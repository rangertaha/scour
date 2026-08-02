// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/export"
	"github.com/rangertaha/scour/internal/query"
	"github.com/rangertaha/scour/internal/store"
)

// registerRecords wires the records an item's crawls produced.
//
// Four routes on two paths, because a record is read one at a time and edited
// in bulk. Marking is a PATCH on the collection carrying a list of ids rather
// than a request per record: review is done a screenful at a time, and forty
// round trips to record forty verdicts is the difference between a reviewer who
// keeps going and one who stops.
//
// There is no /search path. Searching records is reading this collection with a
// query on it, and a second URL returning the same rows in a different order is
// the duplicate the command surface spent its effort removing. `?q=` asks the
// question; everything else narrows the set the same way whether the caller is
// listing, exporting or deleting, so one builder serves all four handlers and a
// filter cannot come to mean one thing on a listing and another on a removal.
//
// It is a method rather than a free function because every handler needs the
// store and the failure helpers hanging off the server. The caller wires it into
// the mux alongside the routes in server.go.
func (s *Server) registerRecords(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/items/{name}/records", s.listRecords)
	mux.HandleFunc("GET /v1/items/{name}/records/{id}", s.showRecord)
	mux.HandleFunc("PATCH /v1/items/{name}/records", s.markRecords)
	mux.HandleFunc("DELETE /v1/items/{name}/records", s.removeRecords)
}

// totalKey is the envelope field carrying the count before the limit, so a
// caller knows what it is sampling. Named for the reason runKey is: the
// listing, the search and the export all read the same rows, and a reader
// keying on "total" would not notice one of them saying something else.
const totalKey = "total"

// listRecords is the record collection: a listing, a search, an export and a
// stream, depending on what was asked for.
//
// One handler for the four because they return the same rows from the same
// query and differ only in what happens to them afterwards. Splitting them
// would mean four copies of the filter parsing, and a filter that worked on the
// listing and was quietly dropped from the export is exactly the kind of fault
// nobody notices until a nightly export is wrong.
func (s *Server) listRecords(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	q, ok := s.recordQuery(w, r, item)
	if !ok {
		return
	}

	// Following is decided before the format, because it overrides one. A
	// stream is ndjson whatever was asked for: the envelope describes a page
	// and a stream has no pages, so a stream carrying one would be lying about
	// its total.
	if boolParam(r, "follow") {
		s.streamRecords(w, r, item, q)
		return
	}

	document := r.URL.Query().Get("format")
	if document != "" && !documentFormat(document) {
		s.badRequest(w, fmt.Sprintf("format %q is not one of: %s",
			document, strings.Join(export.Documents(), ", ")))
		return
	}

	rows, total, err := s.store.SearchRecords(r.Context(), item.ID, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if document != "" {
		s.writeDocument(w, r, document, rows)
		return
	}

	// The envelope is the one this route has always answered with. The design
	// renames it to `data` with a cursor beside it, which cannot be done to a
	// published shape without breaking every client reading `records`, so it is
	// a /v2 change rather than something to slip in under a new file.
	writeJSON(w, http.StatusOK, map[string]any{"records": rows, totalKey: total})
}

// showRecord is one record in full, with its values and the page it came from.
//
// Scoped to the item, so an id copied out of another item's listing is a miss
// rather than somebody else's row shown under the wrong name.
func (s *Server) showRecord(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	id, ok := s.recordID(w, r.PathValue("id"))
	if !ok {
		return
	}
	row, err := s.store.RecordByID(r.Context(), item.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": row})
}

// markRequest is a reviewer's verdict on several records at once.
//
// Ids rather than a filter, because a verdict is a judgement about particular
// rows that a person looked at. A PATCH that marked everything a query matched
// would let a query that drifted between the listing and the marking record
// verdicts on records nobody ever saw.
type markRequest struct {
	IDs     []uint `json:"ids"`
	Verdict string `json:"verdict"`
}

// markRecords records a person's verdict on records already extracted.
//
// This is what the CLI calls `record mark`, and it supersedes POST on
// .../records/{id}/label. That path ended in a verb and could only carry one
// record, and it spoke the older vocabulary where the column's name, label,
// stood for two different things: the words a page might call a property, and a
// judgement about a row. The body says verdict and takes valid, invalid or none.
func (s *Server) markRecords(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	var req markRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		s.badRequest(w, "ids is required: mark the records you looked at, as {\"ids\":[41,42]}")
		return
	}
	label, ok := verdictLabel(req.Verdict)
	if !ok {
		s.badRequest(w, fmt.Sprintf("verdict must be valid, invalid or none, got %q", req.Verdict))
		return
	}

	n, err := s.store.MarkRecords(r.Context(), item.ID, req.IDs, label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, fmt.Sprintf("none of those ids are %s's records", item.Name))
		return
	}
	// Both counts, always. Saying "marked 3" when five ids were sent leaves the
	// caller believing two verdicts were recorded that were not, and a reviewer
	// working from a stale listing is the ordinary way that happens.
	writeJSON(w, http.StatusOK, map[string]any{
		"marked": n, "asked": len(req.IDs), "verdict": verdictWord(label),
	})
}

// removeRecords drops what a filter matched.
//
// A record is what was read out of a page, so the pages stay: removing a bad
// extraction is not a reason to pay to fetch the site again, and the next
// training run reads the same pages back.
//
// An unfiltered DELETE is refused. `DELETE /v1/items/vehicle/records` is a
// plausible typo for the listing beside it and there is no undo, so clearing
// the whole collection is asked for in words with ?all=true. It has to be a
// word rather than an empty filter set, because an empty filter set is also
// what a client building a query string out of variables produces when they are
// all empty.
func (s *Server) removeRecords(w http.ResponseWriter, r *http.Request) {
	item, ok := s.item(w, r)
	if !ok {
		return
	}
	q, ok := s.recordQuery(w, r, item)
	if !ok {
		return
	}
	// A limit says how many rows to show. Honoured here it would delete the
	// highest-confidence handful of what matched and leave the rest, which is an
	// arbitrary subset nobody asked for, so it is refused rather than ignored.
	if q.Limit > 0 {
		s.badRequest(w, "?limit= says how many records to show, so a delete will not take one: "+
			"narrow it with a query or a filter instead")
		return
	}

	all := boolParam(r, "all")
	switch narrowed := narrowedRecords(q); {
	case !narrowed && !all:
		s.badRequest(w, "a delete needs a query or a filter, or ?all=true to mean every record: "+
			"?job=uk, ?q=make:Ford, ?verdict=invalid")
		return
	case narrowed && all:
		s.badRequest(w, "?all=true removes every record, so it takes no query or filter")
		return
	}

	rows, _, err := s.store.SearchRecords(r.Context(), item.ID, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	n, err := s.store.DeleteRecords(r.Context(), item.ID, ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// 200 with the count rather than 204. A filtered delete is the one removal
	// whose size the caller cannot know in advance, and "nothing matched" and
	// "four thousand went" are answers worth telling apart.
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// recordPoll is how often a follower asks for what is new.
//
// The same interval the command line's --follow uses, for the same reason: a
// crawl fetches a few pages a second and only some of them yield a record, so
// anything shorter is mostly empty queries against a database the crawl is
// trying to write to.
const recordPoll = time.Second

// streamRecords sends records as they are extracted, one JSON object per line.
//
// NDJSON rather than server-sent events. SSE buys reconnection with a
// Last-Event-ID and a way to label event types, and there is one kind of event
// here and an id parameter that already resumes a stream exactly, so all it
// would add is a framing every consumer has to strip. NDJSON is what `scour
// --json` prints, so the same jq line reads the CLI's stream and this one.
//
// The stream starts at the end of the table, not the beginning. A stream is
// what lands after the request, and a client that wants a snapshot as well asks
// twice, which is honest about the race between the two: a snapshot bolted onto
// the front of a tail would still miss anything written between the two
// queries, while claiming to be complete. ?since_id= is how a client that was
// disconnected picks up exactly where it stopped.
func (s *Server) streamRecords(w http.ResponseWriter, r *http.Request, item *store.Item, q store.RecordQuery) {
	// Without a flusher every line sits in a buffer until the response ends,
	// and a response that ends is not a stream. Better to refuse than to hand
	// back something that looks like it is working and delivers nothing until
	// the crawl stops.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this server cannot stream a response")
		return
	}

	mark := q.SinceID
	if mark == 0 {
		latest, err := s.store.LatestRecordID(r.Context(), item.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		mark = latest
	}
	// A limit bounds a page and a stream has none, so it is dropped here rather
	// than capping every poll at the same number and silently discarding the
	// records past it.
	q.Limit = 0

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	// Flushed before the first record so the caller sees the status while the
	// crawl is still working, instead of waiting on a connection that looks
	// dead until something happens to match.
	flusher.Flush()

	enc := json.NewEncoder(w)
	tick := time.NewTicker(recordPoll)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			// The client hung up, which is how a stream ends. Nothing can be
			// written to a closed connection and nothing is wrong.
			return
		case <-tick.C:
		}

		q.SinceID = mark
		fresh, _, err := s.store.SearchRecords(r.Context(), item.ID, q)
		if err != nil {
			// The status went out with the header, so there is no way left to
			// report this to the client: an error object written now would
			// arrive as one more line the far end parses as a record. Logged
			// and dropped, and the closing connection is what the client sees.
			slog.Error("record stream failed", "item", item.Name, "err", err)
			return
		}
		for _, row := range fresh {
			if row.ID > mark {
				mark = row.ID
			}
			if err := enc.Encode(row); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

// writeDocument answers with the records as a file would hold them.
//
// The bytes are the exporter's, not this package's. `scour record write` and
// `GET ...?format=csv` are the same row of the parity table, so a client that
// pipes one into a script must be able to pipe the other into the same script,
// and that only stays true while there is one implementation of each encoding.
//
// Built whole before anything is sent, because the alternative is a 200 with
// half a document under it when encoding fails part way. The rows are already
// in memory by this point, so holding their encoding costs a copy of what is
// there rather than a second read of the table.
func (s *Server) writeDocument(w http.ResponseWriter, r *http.Request, document string, rows []store.RecordRow) {
	var buf bytes.Buffer
	if err := export.WriteTo(document, &buf, rows); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", documentType(document))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		slog.Debug("could not write export", "err", err)
	}
}

// documentType is what a browser or a pipe should treat the export as. Unknown
// names never reach here, and a body of an unknown kind is safer described as
// bytes than as anything that might be rendered.
func documentType(document string) string {
	switch document {
	case "csv":
		return "text/csv; charset=utf-8"
	case "json":
		return "application/json; charset=utf-8"
	case "jsonl":
		return "application/x-ndjson"
	default:
		return "application/octet-stream"
	}
}

// documentFormat reports whether a name is an encoding this route can answer in.
//
// The exporters also register a webhook, which is a destination rather than an
// encoding: ?format=webhook would be asking the server to post the records
// somewhere and hand back nothing, which is not what a GET on a collection can
// mean.
func documentFormat(name string) bool {
	for _, known := range export.Documents() {
		if known == name {
			return true
		}
	}
	return false
}

// recordQuery reads the filters off the query string, answering the request
// itself when one of them is wrong.
//
// The parameters are the command line's flags spelled the same way, so anyone
// who knows one surface can guess the other: --confidence is ?confidence=, -j is
// ?job=, --exclude-type is ?exclude_type=. A name is checked rather than
// ignored, because a filter that is quietly dropped answers with more rows than
// were asked for, and a caller deleting what it thinks it filtered would not
// find out until afterwards.
func (s *Server) recordQuery(w http.ResponseWriter, r *http.Request, item *store.Item) (store.RecordQuery, bool) {
	params := r.URL.Query()
	q := store.RecordQuery{
		Formats:       params["type"],
		ExcludeFormat: params["exclude_type"],
		Limit:         limitOf(r, 0),
		SinceID:       uint(intParam(r, "since_id")),
	}

	// Uncapped by default, unlike the runs listing. The parity table maps
	// ?limit= onto --limit, whose default is no cap, and ?format=csv has to
	// answer with the file `record write` would have written rather than with
	// its first page. Bounding by default is MCP's rule, where an unbounded
	// answer costs an agent its own instructions; a program reading HTTP can be
	// trusted to ask for what it wants.

	if !s.confidenceOf(w, params.Get("confidence"), &q) {
		return q, false
	}

	// The old spelling of this filter, refused rather than ignored: a client
	// still sending ?label= is asking for a subset, and answering with every
	// record would look like the item had no marks on it at all.
	if params.Has("label") {
		s.badRequest(w, "?label= is now ?verdict=, and unlabelled is now none")
		return q, false
	}
	if v := params.Get("verdict"); v != "" {
		label, ok := verdictLabel(v)
		if !ok {
			s.badRequest(w, fmt.Sprintf("verdict must be valid, invalid or none, got %q", v))
			return q, false
		}
		q.Label = label
	}

	// A record carries no job. The job is the one that fetched the page it was
	// read out of, which is a fact recorded at fetch time, so the name is
	// resolved to an id here and the store joins through the url. Resolved
	// before anything runs, so an unknown job is a 404 rather than an empty
	// answer that looks like a job which found nothing.
	if name := params.Get("job"); name != "" {
		job, err := s.store.Job(r.Context(), name)
		if err != nil {
			s.fail(w, r, err)
			return q, false
		}
		q.JobID = job.ID
	}

	if !s.termsOf(w, r, item, &q) {
		return q, false
	}
	return q, true
}

// confidenceOf reads ?confidence=, which takes a floor, a ceiling or a band.
//
// A ceiling is not a nicety. Export wants the rows the model was sure about and
// review wants the ones it was not, and a filter that only has a floor can ask
// for the first and not the second, which leaves the records most worth a
// person's attention unselectable.
func (s *Server) confidenceOf(w http.ResponseWriter, raw string, q *store.RecordQuery) bool {
	if raw == "" {
		return true
	}

	floor, ceiling, band := strings.Cut(raw, "..")
	read := func(text string) (float64, bool) {
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("confidence %q is not a number: it takes 0.8, ..0.5 or 0.4..0.6", raw))
			return 0, false
		}
		if v < 0 || v > 1 {
			s.badRequest(w, fmt.Sprintf("confidence %v is outside 0 to 1", v))
			return 0, false
		}
		return v, true
	}

	if floor != "" {
		v, ok := read(floor)
		if !ok {
			return false
		}
		q.MinConfidence = v
	}
	if !band {
		return true
	}
	if ceiling == "" {
		if floor == "" {
			s.badRequest(w, "confidence .. names neither a floor nor a ceiling")
			return false
		}
		// "0.8.." is the floor written the long way, which is already read.
		return true
	}
	v, ok := read(ceiling)
	if !ok {
		return false
	}
	// A ceiling of zero would filter nothing, because zero is how the query
	// says a ceiling was not given. Selecting only the records the model scored
	// at exactly nothing is not what anybody means by it, so it is refused
	// rather than answered with the whole table.
	if v == 0 {
		s.badRequest(w, "a confidence ceiling of 0 selects nothing: ..0.5 is the doubtful half")
		return false
	}
	if v < q.MinConfidence {
		s.badRequest(w, fmt.Sprintf("confidence band %q ends below where it starts", raw))
		return false
	}
	q.MaxConfidence = v
	return true
}

// termsOf reads ?q=, the search in the command line's own syntax.
//
// Parsed against the item's properties rather than against punctuation, which
// is what the language requires: a url is full of colons, so whether the text
// before one is a field name can only be decided by knowing which fields exist.
// That is also what turns a misspelled field into a message naming the real
// ones instead of a search for a literal string nobody meant.
func (s *Server) termsOf(w http.ResponseWriter, r *http.Request, item *store.Item, q *store.RecordQuery) bool {
	words := searchWords(r.URL.Query()["q"])
	if len(words) == 0 {
		return true
	}

	// Loaded whole only when there is a query to parse, because the properties
	// are what the parse needs and a listing has no use for them.
	full, err := s.store.ItemFull(r.Context(), item.Name)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	fields := make([]string, 0, len(full.Properties))
	for _, p := range full.Properties {
		fields = append(fields, p.Name)
	}

	parsed, err := query.Parse(words, fields)
	if err != nil {
		s.badRequest(w, err.Error())
		return false
	}
	q.Terms = parsed.Terms
	return true
}

// searchWords splits ?q= into the terms the parser expects.
//
// On the command line the shell does this, and one argument is one term, so a
// quoted phrase arrives whole. Over HTTP there is no shell and ?q=make:Ford
// "crew cab" is one string, so the quoting is honoured here instead. Without it
// a phrase would arrive as two terms that both have to match anywhere, which is
// a different and much wider question than the one that was asked.
//
// Repeating the parameter works too, and each value is split the same way, so a
// client assembling a query out of parts need not quote them into one string.
func searchWords(values []string) []string {
	var out []string
	for _, value := range values {
		var word strings.Builder
		quoted := false
		flush := func() {
			if word.Len() > 0 {
				out = append(out, word.String())
				word.Reset()
			}
		}
		for _, r := range value {
			switch {
			case r == '"':
				quoted = !quoted
				// A quote that closes an empty phrase still ends a term, so
				// searching for "" is an empty term the parser drops rather
				// than the next word swallowed into this one.
				if !quoted {
					flush()
				}
			case (r == ' ' || r == '\t') && !quoted:
				flush()
			default:
				word.WriteRune(r)
			}
		}
		flush()
	}
	return out
}

// narrowedRecords reports whether a query picks out a subset rather than
// everything the item has. It is what decides whether a delete needs ?all=true,
// so every field that filters has to be listed: one left out is a parameter that
// deletes the whole table while looking like it deleted four rows.
func narrowedRecords(q store.RecordQuery) bool {
	return len(q.Terms) > 0 || len(q.IDs) > 0 || q.JobID > 0 ||
		q.MinConfidence > 0 || q.MaxConfidence > 0 ||
		len(q.Formats) > 0 || len(q.ExcludeFormat) > 0 ||
		q.Label != "" || q.SinceID > 0
}

// recordID reads an id out of the path, answering the request itself when it is
// not a number.
func (s *Server) recordID(w http.ResponseWriter, raw string) (uint, bool) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		s.badRequest(w, "record id must be a number, from GET /v1/items/{name}/records")
		return 0, false
	}
	return uint(id), true
}

// verdictLabel resolves the word a caller uses to the label the column stores.
//
// The two differ by one row and the difference is deliberate. The API and the
// CLI say `none`, because a record without a verdict has not been judged rather
// than been judged neutral; the column has said `unlabelled` since before there
// was a word for the distinction, and renaming it would be a migration for a
// spelling. The older spellings are still accepted, since they are what the
// route this one replaces took.
func verdictLabel(v string) (store.Label, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case string(store.Valid):
		return store.Valid, true
	case string(store.Invalid):
		return store.Invalid, true
	case "none", "unlabelled", "unlabeled":
		return store.Unlabelled, true
	default:
		return "", false
	}
}

// verdictWord is the label as the surfaces say it, so a client reads back the
// same word it would have sent.
func verdictWord(label store.Label) string {
	if label == store.Unlabelled {
		return "none"
	}
	return string(label)
}

// boolParam reads a flag off the query string.
//
// Only the words that mean yes count. A parameter that is present but says
// false is a client deciding not to do something, and reading it as true
// because the key was there would make ?all=false delete every record of an
// item.
func boolParam(r *http.Request, name string) bool {
	switch strings.ToLower(r.URL.Query().Get(name)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}
