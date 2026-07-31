// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/train"
	"github.com/rangertaha/scour/internal/tui"
)

// interval is how often the view re-reads the database.
//
// Fast enough that pressing a key looks like it did something, slow enough that
// watching a crawl is not itself a load on the database the crawl is writing to.
const interval = time.Second

func newTopCmd(a *app) *cli.Command {
	return &cli.Command{
		Category: "SEARCH",
		Name:     "top",
		Usage:    "Monitor engine activity",
		Description: "One screen per fleet: what each item has, how far its crawl has got, how\n" +
			"fast it is going now, and whether it is running.\n\n" +
			"Pausing keeps everything. The frontier holds its order and its leases, so a\n" +
			"resumed crawl carries on rather than starting again, and a crawl paused here\n" +
			"stays paused after this exits.",
		UsageText: "  scour top",
		Action: func(c context.Context, cmd *cli.Command) error {
			s, err := a.Store()
			if err != nil {
				return err
			}
			return runTop(c, a, s)
		},
	}
}

func runTop(ctx context.Context, a *app, s *store.Store) error {
	// The view owns the terminal for as long as it runs, so nothing else may
	// write to it. A crawl started from here logs as any crawl does, and those
	// lines land on top of the table: the screen came out with its bands
	// written over each other and the numbers were right underneath.
	//
	// Logs are silenced rather than redirected, because a file nobody was told
	// about is not better than no file. `scour start` remains the way to watch
	// a crawl's own output.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previous)

	app := tview.NewApplication()

	title := tview.NewTextView().SetDynamicColors(true)
	title.SetBackgroundColor(tcell.ColorDarkCyan)

	header := tview.NewTextView().SetDynamicColors(true)
	table := tview.NewTable().SetBorders(false).SetSelectable(true, false)
	table.SetFixed(1, 0)
	table.SetSelectedStyle(tcell.StyleDefault.
		Background(tcell.ColorDarkCyan).Foreground(tcell.ColorWhite).Bold(true))

	footer := tview.NewTextView().SetDynamicColors(true)
	footer.SetBackgroundColor(tcell.ColorDarkCyan)
	footer.SetText(" [white::b]s[-::-] start   [white::b]x[-::-] stop   " +
		"[white::b]t[-::-] train   [white::b]q[-::-] quit   [white::b]up down[-::-] select")

	// Every band spans the terminal: a Flex row takes the full width, so the
	// coloured title and footer read as bars rather than as text with a stripe
	// behind it.
	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(title, 1, 0, false).
		AddItem(header, 3, 0, false).
		AddItem(table, 0, 1, true).
		AddItem(footer, 1, 0, false)

	// One goroutine reads the database and draws; everything else asks it to.
	//
	// Sharing the snapshot between the ticker, the key handler and a finished
	// crawl meant three goroutines building and rendering it at once, and the
	// screen came out with its bands written over each other. A view is a
	// single writer or it is a race.
	var (
		mu   sync.Mutex
		snap tui.Snapshot
		note string
	)
	setNote := func(msg string) {
		mu.Lock()
		note = msg
		mu.Unlock()
	}
	runner := newRunner(a, setNote)

	// Buffered by one: a refresh already pending is a refresh, so an action
	// never waits and never queues a second redraw behind the first.
	wake := make(chan struct{}, 1)
	refresh := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	draw := func() {
		next, err := tui.Take(ctx, s)
		if err != nil {
			setNote("[red]" + err.Error() + "[-]")
		}
		mu.Lock()
		if err == nil {
			snap = next
		}
		shown, msg := snap, note
		mu.Unlock()

		app.QueueUpdateDraw(func() {
			title.SetText(" [white::b]scour top[-::-]   " + msg)
			renderHeader(header, shown)
			renderTable(table, shown)
		})
	}

	// The selected row is the one acted on, and it is identified by name rather
	// than by index: rows are sorted, so an item added or removed between
	// refreshes would otherwise move the cursor onto something else.
	selected := func() (tui.Row, bool) {
		row, _ := table.GetSelection()
		mu.Lock()
		defer mu.Unlock()
		if row < 1 || row-1 >= len(snap.Rows) {
			return tui.Row{}, false
		}
		return snap.Rows[row-1], true
	}

	// Starting clears the pause and, if nothing else is fetching for the
	// item, begins a crawl here. Stopping sets it, which halts a crawl in
	// this process and takes the item out of what a dispatcher elsewhere is
	// offered: one key, whichever topology is running.
	act := func(start bool) {
		r, ok := selected()
		if !ok {
			return
		}
		if err := s.SetPaused(ctx, r.ItemID, !start); err != nil {
			setNote("[red]" + err.Error() + "[-]")
			refresh()
			return
		}
		switch {
		case !start:
			setNote(fmt.Sprintf("[orange]stopping %s[-]", r.Name))
		case r.State == tui.StateRunning:
			setNote(fmt.Sprintf("[green]%s is already running[-]", r.Name))
		default:
			setNote(fmt.Sprintf("[green]starting %s[-]", r.Name))
			go func(name string) {
				runner.start(ctx, name)
				refresh()
			}(r.Name)
		}
		refresh()
	}

	table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 'q':
			app.Stop()
			return nil
		case 's':
			act(true)
			return nil
		case 'x':
			act(false)
			return nil
		case 't':
			r, ok := selected()
			if !ok {
				return nil
			}
			setNote(fmt.Sprintf("[green]training %s[-]", r.Name))
			go func(name string) {
				runner.train(ctx, name)
				refresh()
			}(r.Name)
			refresh()
			return nil
		}
		return ev
	})

	go func() {
		draw()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				app.Stop()
				return
			case <-tick.C:
				draw()
			case <-wake:
				draw()
			}
		}
	}()

	return app.SetRoot(layout, true).EnableMouse(false).Run()
}

// renderHeader draws the fleet totals.
func renderHeader(v *tview.TextView, s tui.Snapshot) {
	queued, visited, records, rate := s.Totals()
	v.SetText(fmt.Sprintf(
		"\n fetching [yellow]%.1f/s[-]   queued [yellow]%s[-]"+
			"   fetched [yellow]%s[-]   records [yellow]%s[-]\n %s",
		rate, count(queued), count(visited), count(records), bar(rate)))
}

// bar is the fetch rate as something to glance at rather than read.
func bar(rate float64) string {
	const width, full = 40, 20.0
	on := int(rate / full * width)
	if on > width {
		on = width
	}
	return "[green]" + strings.Repeat("█", on) + "[-]" + strings.Repeat("░", width-on)
}

var columns = []string{"NAME", "TARGETS", "QUEUED", "FETCHED", "RECORDS", "RULES", "RATE", "STATE"}

// renderTable draws one row per item.
func renderTable(t *tview.Table, s tui.Snapshot) {
	t.Clear()
	for i, name := range columns {
		align := tview.AlignRight
		if i == 0 || name == "STATE" {
			align = tview.AlignLeft
		}
		cell := tview.NewTableCell(" " + name).
			SetTextColor(tcell.ColorYellow).
			SetAlign(align).
			SetSelectable(false)
		// The name takes the slack, so the table fills the terminal instead of
		// hugging its widest number and leaving the rest of the screen blank.
		if i == 0 {
			cell.SetExpansion(1)
		}
		t.SetCell(0, i, cell)
	}

	for i, r := range s.Rows {
		cells := []string{
			" " + r.Name, count(r.Targets), count(r.Queued), count(r.Visited),
			count(r.Records), count(r.Rules), fmt.Sprintf("%.1f/s", r.Rate),
			" " + string(r.State),
		}
		for c, text := range cells {
			align := tview.AlignRight
			if c == 0 || c == len(cells)-1 {
				align = tview.AlignLeft
			}
			cell := tview.NewTableCell(text + " ").SetAlign(align)
			if c == 0 {
				cell.SetExpansion(1)
			}
			if c == len(cells)-1 {
				cell.SetTextColor(stateColour(r.State))
			}
			t.SetCell(i+1, c, cell)
		}
	}
}

func stateColour(s tui.State) tcell.Color {
	switch s {
	case tui.StateRunning:
		return tcell.ColorGreen
	case tui.StatePaused:
		return tcell.ColorOrange
	case tui.StateIdle:
		return tcell.ColorGray
	default:
		return tcell.ColorDefault
	}
}

// count groups thousands, because six digits of frontier is unreadable without.
func count(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// runner starts crawls and trainings on behalf of the view.
//
// It keeps the items it has work in flight for, so pressing start twice does
// not run two crawls over one frontier. A crawl started elsewhere is not
// visible here, which is why start also clears the pause: that is the part that
// works whoever is doing the fetching.
type runner struct {
	app  *app
	mu   sync.Mutex
	busy map[string]bool
	note func(string)
}

func newRunner(a *app, note func(string)) *runner {
	return &runner{app: a, busy: map[string]bool{}, note: note}
}

// claim reserves an item, reporting whether the caller got it.
func (r *runner) claim(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy[name] {
		return false
	}
	r.busy[name] = true
	return true
}

func (r *runner) release(name string) {
	r.mu.Lock()
	delete(r.busy, name)
	r.mu.Unlock()
}

// start crawls one item with the configured defaults.
//
// No page budget: the view is how a crawl is stopped now, and a budget chosen
// here would be a number nobody asked for. The item's own depth and content
// types still apply, because those describe the site rather than this run.
func (r *runner) start(ctx context.Context, name string) {
	if !r.claim(name) {
		return
	}
	defer r.release(name)

	s, err := r.app.Store()
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	item, err := s.ItemFull(ctx, name)
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}

	types, err := content.New(itemTypes(item), nil)
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	scorer, _, err := train.Scorer(r.app.cfg, item)
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	scorer, _, err = train.ChainScorer(ctx, s, item, scorer)
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	pages, err := r.app.Pages()
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}

	_, err = crawl.New(r.app.cfg, s, pages).Run(ctx, crawl.Options{
		Item:    item,
		Targets: item.Targets,
		Types:   types,
		Depth:   r.app.cfg.Crawl.Depth,
		Scorer:  scorer,
	})
	if err != nil {
		r.note(fmt.Sprintf("[red]%s: %v[-]", name, err))
		return
	}
	r.note(fmt.Sprintf("[green]%s finished crawling[-]", name))
}

// train induces rules from what has already been fetched.
func (r *runner) train(ctx context.Context, name string) {
	if !r.claim(name) {
		return
	}
	defer r.release(name)

	s, err := r.app.Store()
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	pages, err := r.app.Pages()
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	item, err := s.ItemFull(ctx, name)
	if err != nil {
		r.note("[red]" + err.Error() + "[-]")
		return
	}
	result, err := train.New(r.app.cfg, s, pages).Run(ctx, item, train.Options{})
	if err != nil {
		r.note(fmt.Sprintf("[red]%s: %v[-]", name, err))
		return
	}
	r.note(fmt.Sprintf("[green]%s: %d rules, %d records[-]", name, result.Rules, result.Records))
}

// itemTypes is the item's content types, or the configured default when it
// has none of its own.
func itemTypes(e *store.Item) []string {
	if len(e.ContentTypes) == 0 {
		return nil
	}
	out := make([]string, 0, len(e.ContentTypes))
	for _, t := range e.ContentTypes {
		out = append(out, t.Type)
	}
	return out
}
