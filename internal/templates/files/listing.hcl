# A directory crawler: things that appear in a list, each with a page of its
# own. Job adverts, venues, members, courses.
#
# The shape that makes this different from a news site is that the index page
# is worth as much as the detail page, because it holds most of the fields
# already. Keep max_depth low: a listing site is wide rather than deep.

job {{.Name | quote}} {
  start    = ["https://directory.example/listings"]
  domains  = ["directory.example"]
  included = ["*/listing/*", "*/listings*"]

  item "listing" {
    description = "One entry in the directory"

    property "url" {
      type       = str
      required   = true
      aliases    = ["canonical", "link"]
      transforms = [absurl]
    }

    property "title" {
      type       = str
      required   = true
      aliases    = ["name", "headline", "og:title"]
      transforms = [text, trim]
    }

    property "organisation" {
      type       = str
      aliases    = ["company", "employer", "provider", "hiringOrganization"]
      transforms = [text, trim]
    }

    property "location" {
      type       = str
      aliases    = ["place", "address", "jobLocation", "addressLocality"]
      transforms = [text, trim]
    }

    property "posted" {
      type       = date
      aliases    = ["datePosted", "published", "created"]
      transforms = [datetime]
    }

    property "closes" {
      type       = date
      aliases    = ["validThrough", "deadline", "expires"]
      transforms = [datetime]
    }

    property "description" {
      type       = str
      aliases    = ["summary", "og:description", "content"]
      transforms = [text, normalise_space]
    }
  }

  scheduler {
    policy      = "breadth"
    rate        = "2s"
    concurrency = 2
    max_depth   = 2
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

  exporter "json" "listing" {
    dir = "./out"
  }
}
