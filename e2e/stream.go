// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// streamTick is how often the streaming endpoints emit. Fast enough that a
// test does not wait on it, slow enough that a client sees separate messages
// rather than one burst.
const streamTick = 60 * time.Millisecond

// streamEvents is how many an endpoint sends before closing. Bounded, because
// a fixture that never finishes is a fixture every test has to time out.
const streamEvents = 5

// registerStreams adds the two endpoints that answer over time rather than at
// once.
//
// Both matter to a crawler for the same reason and in opposite directions. A
// fetch expects a body that ends; these do not end until they choose to, so
// anything that reads to EOF without a bound will sit here. And the content
// only exists in the stream, so a crawler that gets a body and stops has an
// empty page from a URL that was serving data the whole time.
func registerStreams(mux *http.ServeMux) {
	mux.HandleFunc("GET /stream/events", serverSentEvents)
	mux.HandleFunc("GET /stream/ws", webSocket)
	mux.HandleFunc("GET /stream/", streamIndex)
}

// serverSentEvents is a text/event-stream of berth updates.
//
// Written by hand rather than with a library because the format is four lines
// and the point is that it is not JSON in a single response: an id, an event
// name, a data line, and a blank line that ends the message.
func serverSentEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for i := 1; i <= streamEvents; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(streamTick):
		}

		payload, err := json.Marshal(map[string]any{
			"berth":   i,
			"status":  berthStatus(i),
			"vessel":  fmt.Sprintf("MV Ardmore %d", i),
			"page":    PageHarbour,
			"updated": "2025-07-16T09:0" + fmt.Sprint(i) + ":00Z",
		})
		if err != nil {
			return
		}
		// A link in the comment field, so even the stream says where to go
		// next. Comments are the one part of SSE a client may ignore, which
		// makes it the right place for something optional.
		fmt.Fprintf(w, ": see "+PageHarbour+"\nid: %d\nevent: berth\ndata: %s\n\n", i, payload)
		flusher.Flush()
	}
	fmt.Fprint(w, "event: done\ndata: {\"next\":\"/stream/ws\"}\n\n")
	flusher.Flush()
}

// webSocket upgrades and sends the same updates as frames.
//
// The handshake is the whole difficulty: a URL that answers 101 rather than
// 200 is one a plain fetch cannot read at all, and a crawler that treats the
// upgrade as a failed page will record it as an error rather than as a kind of
// endpoint it does not speak.
func webSocket(w http.ResponseWriter, r *http.Request) {
	// A crawler sends a plain GET, and that is the common case here rather than
	// the exception. Answering it as a page says what the URL is instead of
	// attempting an upgrade that cannot work and leaving the connection half
	// open, which is what made a single unupgraded request hang for minutes.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUpgradeRequired)
		fmt.Fprint(w, `<html><head><meta charset="utf-8"><title>WebSocket endpoint</title></head><body>
<h1>This is a WebSocket</h1>
<p>It speaks frames, not pages. The same data is available as
<a href="/stream/events">server-sent events</a>, and the place it describes is
<a href="/places/harbour.html">North Harbour</a>.</p>
</body></html>`)
		return
	}

	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	defer conn.Close()

	for i := 1; i <= streamEvents; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(streamTick):
		}

		payload, err := json.Marshal(map[string]any{
			"berth":  i,
			"status": berthStatus(i),
			"page":   PageHarbour,
		})
		if err != nil {
			return
		}
		if err := wsutil.WriteServerText(conn, payload); err != nil {
			return
		}
	}
	_ = wsutil.WriteServerText(conn, []byte(`{"done":true,"next":"/stream/events"}`))
}

func berthStatus(i int) string {
	if i%2 == 0 {
		return "occupied"
	}
	return "free"
}

// streamIndex is an ordinary page describing the two, so the streaming
// endpoints are reachable by following a link like everything else.
func streamIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!-- The streaming endpoints, linked from an ordinary page so a crawl can
     reach them by following an href and find out what they are. -->
<html lang="en"><head><meta charset="utf-8"><title>Live berth data</title></head><body>
<h1>Live berth data</h1>
<ul>
  <li><a href="/stream/events">Server-sent events</a>, text/event-stream, `+fmt.Sprint(streamEvents)+` messages then done</li>
  <li><a href="/stream/ws">WebSocket</a>, answers 101 rather than 200</li>
</ul>
<p>The place these describe is <a href="../places/harbour.html">North Harbour</a>.</p>
</body></html>`)
}
