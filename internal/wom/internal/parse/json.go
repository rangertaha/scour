// SPDX-License-Identifier: MIT

package parse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parseJSON builds a value tree under doc from a JSON body. Objects become
// graph.KindObject with one graph.KindField child per member, arrays become graph.KindArray with
// their elements as direct children, and scalars become graph.KindValue. That shape
// is what lets Node.Path emit a JSONPath for any node.
//
// Bodies holding several concatenated documents (JSON Lines, or a stream of
// objects) are decoded in full, each document becoming a child of doc.
func parseJSON(doc *graph.Node, body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var decoded int
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if decoded > 0 {
				// Trailing garbage after valid documents: keep what parsed.
				break
			}
			return fmt.Errorf("parse json: %w", err)
		}
		buildJSON(doc, "", v)
		decoded++
	}
	if decoded == 0 {
		return fmt.Errorf("parse json: %w", io.ErrUnexpectedEOF)
	}
	return nil
}

// buildJSON attaches v to parent and returns the node that was created for
// it. When key is non-empty the value is wrapped in a graph.KindField node naming
// it, and the returned node is that wrapper.
func buildJSON(parent *graph.Node, key string, v any) *graph.Node {
	created := parent
	if key != "" {
		created = parent.Append(graph.New(graph.KindField, key, ""))
		parent = created
	}

	switch t := v.(type) {
	case map[string]any:
		obj := parent.Append(graph.New(graph.KindObject, "", ""))
		// Go randomizes map iteration; sort so a document always produces the
		// same tree and therefore the same paths.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			buildJSON(obj, k, t[k])
		}
		if key == "" {
			created = obj
		}
	case []any:
		arr := parent.Append(graph.New(graph.KindArray, "", ""))
		for i, item := range t {
			// Array indices are positional across the whole array, so they
			// cannot use the per-kind sibling counter that Append assigns:
			// a mixed array such as [{}, "x", {}] would otherwise number the
			// second object 2 instead of 3.
			if child := buildJSON(arr, "", item); child != nil {
				child.SetIndex(i + 1)
			}
		}
		if key == "" {
			created = arr
		}
	default:
		val := parent.Append(graph.New(graph.KindValue, "", jsonScalar(t)))
		if key == "" {
			created = val
		}
	}
	return created
}

// jsonScalar renders a decoded JSON scalar as the text wom matches against.
func jsonScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
