// SPDX-License-Identifier: GPL-3.0-or-later

package files_test

import (
	"context"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/exporter/exportertest"

	_ "github.com/rangertaha/scour/internal/exporter/files"
)

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = object

      property "name" {
        type = str
      }
    }
  }
` + blocks + `
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

// TestContract runs each of the three file formats through the shared suite.
//
// They had no tests of their own at all, which is how three of the six
// exporters came to be the ones without a `closed` guard.
func TestContract(t *testing.T) {
	for _, format := range []string{"json", "jsonlines", "csv"} {
		t.Run(format, func(t *testing.T) {
			exportertest.Run(t, func(t *testing.T, dir string) exporter.Exporter {
				set, err := exporter.New(context.Background(), job(t, `
  exporter "`+format+`" "article" {
    dir = "`+dir+`"
  }
`), nil)
				if err != nil {
					t.Fatalf("new: %v", err)
				}
				return set
			})
		})
	}
}
