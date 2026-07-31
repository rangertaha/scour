// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metrics counts requests, in the Prometheus text format.
//
// Hand-written rather than pulled from a client library because the exposition
// format is a stable, documented handful of lines and this exports a handful of
// series. A dependency here would be larger than the thing it replaced.
type Metrics struct {
	started time.Time

	mu       sync.Mutex
	requests map[requestKey]int64
	duration map[requestKey]float64
	inFlight int64
}

type requestKey struct {
	method string
	status int
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{
		started:  time.Now(),
		requests: map[requestKey]int64{},
		duration: map[requestKey]float64{},
	}
}

// recorder captures the status code, which net/http does not otherwise expose
// once the handler has written it.
type recorder struct {
	http.ResponseWriter
	status int
	// written is the response size, which the access log reports and net/http
	// no more exposes after the fact than it does the status.
	written int64
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write records the implicit 200 that writing without WriteHeader produces.
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it supports it, which streaming
// responses need.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware counts every request that passes through it.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		m.mu.Lock()
		m.inFlight++
		m.mu.Unlock()

		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		key := requestKey{method: r.Method, status: rec.status}

		m.mu.Lock()
		m.requests[key]++
		m.duration[key] += time.Since(start).Seconds()
		m.inFlight--
		m.mu.Unlock()
	})
}

// metrics serves the exposition format.
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.stats.Write(w, s.jobs)
}

// Write emits the current values.
func (m *Metrics) Write(w io.Writer, jobs *Jobs) {
	m.mu.Lock()
	requests := make(map[requestKey]int64, len(m.requests))
	duration := make(map[requestKey]float64, len(m.duration))
	for k, v := range m.requests {
		requests[k] = v
	}
	for k, v := range m.duration {
		duration[k] = v
	}
	inFlight := m.inFlight
	started := m.started
	m.mu.Unlock()

	keys := make([]requestKey, 0, len(requests))
	for k := range requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].method != keys[b].method {
			return keys[a].method < keys[b].method
		}
		return keys[a].status < keys[b].status
	})

	fmt.Fprint(w, "# HELP scour_http_requests_total Requests handled, by method and status.\n")
	fmt.Fprint(w, "# TYPE scour_http_requests_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(w, "scour_http_requests_total{method=%q,status=%q} %d\n",
			k.method, strconv.Itoa(k.status), requests[k])
	}

	fmt.Fprint(w, "# HELP scour_http_request_duration_seconds_total Time spent handling requests.\n")
	fmt.Fprint(w, "# TYPE scour_http_request_duration_seconds_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(w, "scour_http_request_duration_seconds_total{method=%q,status=%q} %g\n",
			k.method, strconv.Itoa(k.status), duration[k])
	}

	fmt.Fprint(w, "# HELP scour_http_requests_in_flight Requests currently being handled.\n")
	fmt.Fprint(w, "# TYPE scour_http_requests_in_flight gauge\n")
	fmt.Fprintf(w, "scour_http_requests_in_flight %d\n", inFlight)

	var running, done, failed int
	for _, job := range jobs.List() {
		switch job.State {
		case Running:
			running++
		case Done:
			done++
		case Failed:
			failed++
		}
	}

	fmt.Fprint(w, "# HELP scour_jobs Jobs tracked by this process, by state.\n")
	fmt.Fprint(w, "# TYPE scour_jobs gauge\n")
	fmt.Fprintf(w, "scour_jobs{state=\"running\"} %d\n", running)
	fmt.Fprintf(w, "scour_jobs{state=\"done\"} %d\n", done)
	fmt.Fprintf(w, "scour_jobs{state=\"failed\"} %d\n", failed)

	fmt.Fprint(w, "# HELP scour_uptime_seconds Seconds since this process started serving.\n")
	fmt.Fprint(w, "# TYPE scour_uptime_seconds gauge\n")
	fmt.Fprintf(w, "scour_uptime_seconds %g\n", time.Since(started).Seconds())
}
