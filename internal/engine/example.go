// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"strings"
)

// Example returns a starter job document, commented.
//
// It lives here rather than in the command line because the document is this
// package's language, and because a test here validates it: a sample that does
// not parse is worse than none, since the first thing anybody does with it is
// assume it works.
//
// It is deliberately small. Everything left out has a default, and the point of
// a starting document is to be read in one sitting and then grown, not to be a
// catalogue of what is possible with most of it commented out.
func Example(name string) []byte {
	if strings.TrimSpace(name) == "" {
		name = "example"
	}

	return fmt.Appendf(nil, `# A scour job. Everything a crawl needs is in here, so this document is the
# whole of what it does: nothing is inherited from the server that runs it.
#
#   scour validate %[1]s.hcl    check it
#   scour show     %[1]s.hcl    see it with every default filled in
#   scour spec     %[1]s.hcl    see what the extractor is handed

job %[1]q {
  # Where the crawl starts, and how far it may wander.
  start    = ["https://example.com/"]
  domains  = ["example.com"]
  included = ["*.example.com"]
  excluded = []

  # What to pull out of a page. Aliases are the other names a field goes by,
  # which is what lets it be found on a site that calls it something else.
  item "article" {
    property "url" {
      type       = str
      required   = true
      aliases    = ["uri", "link"]
      transforms = [absurl]
    }

    property "title" {
      type       = str
      required   = true
      aliases    = ["headline"]
      transforms = [text, trim]
    }

    property "published" {
      type       = date
      aliases    = ["pubdate", "datePublished"]
      transforms = [datetime]
    }

    property "body" {
      type       = str
      aliases    = ["content", "articleBody"]
      transforms = [text, normalise_space]
    }
  }

  # The frontier: what is fetched next, and how hard one host is leaned on.
  # Politeness lives here because a rate is per host and shared between jobs.
  scheduler {
    policy      = "priority"
    rate        = "2s"
    concurrency = 2
    max_depth   = 3
    max_pages   = 100
  }

  # An attribute is what the downloader always does. A plugin is something you
  # added to it, and can be reordered or turned off.
  downloader {
    robots     = true
    user_agent = "scour"

    plugin "cache" {
      backend = "local"
      dir     = ".scour/cache"
    }
  }

  # Each exporter writes one item. The second label says which.
  exporter "json" "article" {
    dir = "./out"
  }
}
`, name)
}
