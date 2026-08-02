// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rangertaha/scour/internal/registry"
	"github.com/rangertaha/scour/internal/store"
)

// documents are the encodings that turn records into a document, held in one
// table because two things need them: the exporter registry below, and WriteTo,
// which hands the same bytes to something that is not a file.
//
// The HTTP API serves `?format=csv` out of this, and the promise it makes is
// that the bytes are the ones `scour record write` produces. That promise only
// holds while there is one implementation of each encoding, so a second copy
// living next to the server would be a copy free to drift, and the drift would
// show up as an export that parses in one pipeline and not in the other.
var documents = map[string]format{
	"csv":   csvWriter{},
	"json":  jsonWriter{},
	"jsonl": jsonlWriter{},
}

func init() {
	// Named one at a time rather than looped over the table above, because the
	// documentation check reads these literals to prove that every format the
	// docs offer is one something really registers. A loop over the map hides
	// the names from it, and the check goes quiet rather than wrong, which is
	// the worst way for a check to fail.
	Register("csv", document("csv"))
	Register("json", document("json"))
	Register("jsonl", document("jsonl"))
}

// document builds the exporter for one of the encodings above.
//
// It looks the encoding up rather than closing over it so that the registered
// name and the table cannot come apart: a name registered here that the table
// does not have fails when somebody exports, saying which name, instead of
// writing files through a nil writer.
func document(name string) registry.Factory[Config, Exporter] {
	return func(cfg Config) (Exporter, error) {
		f, ok := documents[name]
		if !ok {
			return nil, fmt.Errorf("no %s writer: records are written as %s",
				name, strings.Join(Documents(), ", "))
		}
		return newFile(cfg, f)
	}
}

// Documents names the encodings WriteTo accepts, sorted.
//
// Not Names, which is every registered exporter and includes the webhook. A
// webhook is a destination rather than an encoding, so it has nothing to say
// when the question is what a set of records should look like on the way out.
func Documents() []string {
	out := make([]string, 0, len(documents))
	for name := range documents {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// WriteTo encodes records somewhere that is not a file.
//
// An Exporter writes files, because that is what an export is on the command
// line. An HTTP response is one stream with no directory to group into, so it
// needs the encoding without the filing, and this is that encoding: the same
// writer, so a caller asking the API for csv gets what `record write` wrote.
//
// Grouping is the one thing that does not survive. A file export writes one
// file per domain and each file's columns are the union of that domain's
// records; here every row is in one document, so the columns are the union of
// all of them. The two agree exactly whenever the records came from one site,
// and where they do not it is because a single response cannot be several files.
func WriteTo(name string, w io.Writer, rows []store.RecordRow) error {
	f, ok := documents[name]
	if !ok {
		return fmt.Errorf("no %s document: records are written as %s",
			name, strings.Join(Documents(), ", "))
	}
	return f.write(w, rows)
}

// format writes one domain's records in a particular encoding.
type format interface {
	ext() string
	write(w io.Writer, rows []store.RecordRow) error
}

// file exports to `<dir>/<item>/<domain>/<timestamp>.<ext>`, which is the
// layout the documentation promises.
type file struct {
	dir       string
	timestamp string
	format    format
}

func newFile(cfg Config, f format) (Exporter, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("exporting to %s needs a directory", f.ext())
	}
	if cfg.Timestamp == "" {
		return nil, fmt.Errorf("exporting needs a timestamp to name the file")
	}
	return &file{dir: cfg.Dir, timestamp: cfg.Timestamp, format: f}, nil
}

// Name implements [Exporter].
func (f *file) Name() string { return f.format.ext() }

// Export implements [Exporter].
func (f *file) Export(ctx context.Context, item string, rows []store.RecordRow) (*Result, error) {
	result := &Result{}
	groups := byDomain(rows)

	for _, domain := range domains(groups) {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		dir := filepath.Join(f.dir, safe(item), safe(domain))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return result, fmt.Errorf("create export directory: %w", err)
		}

		path := filepath.Join(dir, f.timestamp+"."+f.format.ext())
		out, err := os.Create(path) //nolint:gosec // the path is assembled from the configured directory
		if err != nil {
			return result, fmt.Errorf("create %s: %w", path, err)
		}

		err = f.format.write(out, groups[domain])
		closeErr := out.Close()
		if err != nil {
			return result, err
		}
		if closeErr != nil {
			return result, fmt.Errorf("write %s: %w", path, closeErr)
		}

		result.Records += len(groups[domain])
		result.Destinations = append(result.Destinations, path)
	}
	return result, nil
}

// safe makes a name usable as a path segment.
//
// Item names and domains both reach this from user input, and a name
// containing a separator would otherwise write outside the export directory.
func safe(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)

	// "." and ".." survive the mapping above and both escape the directory.
	cleaned = strings.TrimLeft(cleaned, ".")
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}

type csvWriter struct{}

func (csvWriter) ext() string { return "csv" }

// write emits one row per record.
//
// The fixed columns come first and the properties after, so a consumer can
// rely on the shape without knowing the schema. `label` is among them because
// an export is also how a human corrects records outside scour.
func (csvWriter) write(w io.Writer, rows []store.RecordRow) error {
	props := columns(rows)

	out := csv.NewWriter(w)
	header := append([]string{"id", "url", "confidence", "format", "label"}, props...)
	if err := out.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, row := range rows {
		record := []string{
			strconv.FormatUint(uint64(row.ID), 10),
			row.URL,
			strconv.FormatFloat(row.Confidence, 'f', 4, 64),
			row.Format,
			string(row.Label),
		}
		for _, name := range props {
			record = append(record, row.Values[name])
		}
		if err := out.Write(record); err != nil {
			return fmt.Errorf("write record %d: %w", row.ID, err)
		}
	}

	out.Flush()
	return out.Error()
}

type jsonWriter struct{}

func (jsonWriter) ext() string { return "json" }

// jsonRecord is the exported shape. An explicit empty values object rather
// than null, so a consumer can index it without a nil check.
//
// It is flatter than the database row on purpose: an export is read by
// something that does not know scour's tables, and internal ids for the item
// and the URL row would mean nothing to it.
type jsonRecord struct {
	ID         uint              `json:"id"`
	URL        string            `json:"url"`
	Confidence float64           `json:"confidence"`
	Format     string            `json:"format"`
	Label      string            `json:"label"`
	Values     map[string]string `json:"values"`
}

func (jsonWriter) write(w io.Writer, rows []store.RecordRow) error {
	out := make([]jsonRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, exported(row))
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode records: %w", err)
	}
	return nil
}

// jsonlWriter is one record per line, the same shape json writes as an array.
//
// Worth having as well as json because the two are read differently: an array
// has to be parsed whole before anything in it can be used, and a file of lines
// can be streamed, split, or fed to something a line at a time. That matters at
// the size an export actually reaches, and it is the format every log pipeline
// already expects.
type jsonlWriter struct{}

func (jsonlWriter) ext() string { return "jsonl" }

func (jsonlWriter) write(w io.Writer, rows []store.RecordRow) error {
	// No indenting and no array: a line that wraps is a line that cannot be
	// split on newlines, which is the whole point of the format.
	enc := json.NewEncoder(w)
	for _, row := range rows {
		if err := enc.Encode(exported(row)); err != nil {
			return err
		}
	}
	return nil
}

// exported is the flat shape both json formats write.
func exported(row store.RecordRow) jsonRecord {
	values := row.Values
	if values == nil {
		values = map[string]string{}
	}
	return jsonRecord{
		ID:         row.ID,
		URL:        row.URL,
		Confidence: row.Confidence,
		Format:     row.Format,
		Label:      string(row.Label),
		Values:     values,
	}
}
