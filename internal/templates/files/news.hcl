# A news crawler.
#
# The properties are the ones a news site is most likely to mark up, under the
# names it is most likely to use. Nothing here is site-specific: run
# `scour job train` over a few hundred cached pages and it will propose the xpath
# and css that actually work on the site in front of you.

job {{.Name | quote}} {
  start    = ["https://example.com/news/"]
  domains  = ["example.com"]

  # Section indexes and tag pages are worth crawling but rarely worth
  # extracting. Leaving them in scope and letting the scorer rank them is
  # usually better than excluding them by hand.
  excluded = ["*/tag/*", "*/author/*"]

  item "article" {
    description = "One story"

    property "url" {
      type       = str
      required   = true
      aliases    = ["canonical", "uri", "link"]
      transforms = [absurl]
    }

    property "title" {
      type       = str
      required   = true
      aliases    = ["headline", "og:title"]
      transforms = [text, trim]
    }

    property "summary" {
      type       = str
      aliases    = ["description", "og:description", "excerpt", "standfirst"]
      transforms = [text, trim]
    }

    property "author" {
      type = object

      property "name" {
        type       = str
        aliases    = ["byline", "creator"]
        transforms = [text, trim]
      }

      property "profile" {
        type       = url
        transforms = [absurl]
      }
    }

    property "published" {
      type       = date
      aliases    = ["pubdate", "datePublished", "article:published_time"]
      transforms = [datetime]
    }

    property "modified" {
      type       = date
      aliases    = ["dateModified", "article:modified_time"]
      transforms = [datetime]
    }

    property "section" {
      type       = str
      aliases    = ["category", "article:section"]
      transforms = [text, trim]
    }

    property "body" {
      type       = str
      required   = true
      aliases    = ["content", "articleBody"]
      transforms = [text, normalise_space]
    }
  }

  scheduler {
    policy      = "priority"
    rate        = "2s"
    concurrency = 2
    max_depth   = 4
    max_pages   = 5000
  }

  downloader {
    robots     = true
    user_agent = "scour"

    plugin "cache" {
      backend = "local"
      dir     = ".scour/cache"
    }
  }

  pipeline {
    step "clean" "article" {}

    step "dedupe" "article" {
      requires = [clean.article]
    }
  }

  exporter "json" "article" {
    dir = "./out"
  }
}
