// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/tui"
)

// refresh is how often the view re-reads the database.
//
// Fast enough that pressing a key looks like it did something, slow enough that
// watching a crawl is not itself a load on the database the crawl is writing to.
const refresh = time.Second

func newTopCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "top",
		Short: "Watch every entity live, and pause or resume a crawl",
		Long: "One screen per fleet: what each entity has, how far its crawl has got, how\n" +
			"fast it is going now, and whether it is running.\n\n" +
			"Pausing keeps everything. The frontier holds its order and its leases, so a\n" +
			"resumed crawl carries on rather than starting again, and a crawl paused here\n" +
			"stays paused after this exits.",
		Example: "  scour top",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.Store()
			if err != nil {
				return err
			}
			return runTop(ctx(cmd), s)
		},
	}
}

func runTop(ctx context.Context, s *store.Store) error {
	app := tview.NewApplication()

	header := tview.NewTextView().SetDynamicColors(true)
	table := tview.NewTable().SetBorders(false).SetSelectable(true, false)
	footer := tview.NewTextView().SetDynamicColors(true)
	footer.SetText(" [::b]p[::-] pause   [::b]r[::-] resume   [::b]q[::-] quit   " +
		"[::b]up down[::-] select")

	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 3, 0, false).
		AddItem(table, 0, 1, true).
		AddItem(footer, 1, 0, false)

	var snap tui.Snapshot

	draw := func() {
		next, err := tui.Take(ctx, s)
		if err != nil {
			header.SetText(fmt.Sprintf("\n [red]%v[-]", err))
			return
		}
		snap = next
		app.QueueUpdateDraw(func() {
			renderHeader(header, snap)
			renderTable(table, snap)
		})
	}

	// The selected row is the one acted on, and it is identified by name rather
	// than by index: rows are sorted, so an entity added or removed between
	// refreshes would otherwise move the cursor onto something else.
	selected := func() (tui.Row, bool) {
		row, _ := table.GetSelection()
		if row < 1 || row-1 >= len(snap.Rows) {
			return tui.Row{}, false
		}
		return snap.Rows[row-1], true
	}

	setPaused := func(paused bool) {
		r, ok := selected()
		if !ok {
			return
		}
		if err := s.SetPaused(ctx, r.EntityID, paused); err != nil {
			header.SetText(fmt.Sprintf("\n [red]%v[-]", err))
			return
		}
		go draw()
	}

	table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 'q':
			app.Stop()
			return nil
		case 'p':
			setPaused(true)
			return nil
		case 'r':
			setPaused(false)
			return nil
		}
		return ev
	})

	go func() {
		draw()
		tick := time.NewTicker(refresh)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				app.Stop()
				return
			case <-tick.C:
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
		"\n [::b]scour[::-]   fetching [yellow]%.1f/s[-]   queued [yellow]%s[-]"+
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

// renderTable draws one row per entity.
func renderTable(t *tview.Table, s tui.Snapshot) {
	t.Clear()
	for i, name := range columns {
		align := tview.AlignRight
		if i == 0 || name == "STATE" {
			align = tview.AlignLeft
		}
		t.SetCell(0, i, tview.NewTableCell(" "+name).
			SetTextColor(tcell.ColorYellow).
			SetAlign(align).
			SetSelectable(false))
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
