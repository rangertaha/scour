# A scour job. Everything a crawl needs is in here, so this document is the
# whole of what it does: nothing is inherited from the server that runs it.
#
#   scour job valid {{.Name}}.hcl               check it
#   scour job show --file {{.Name}}.hcl         see it with every default filled in
#   scour scrape {{.Name}}.hcl                  run one page and see what came out

job {{.Name | quote}} {
  # Where the crawl starts, and how far it may wander.
  # `domains` is the site, and a subdomain of it counts. `included` is a
  # narrower thing: if you give any pattern at all, every URL has to match one
  # of them, which is how a scope ends up refusing the page you started from.
  start    = ["https://example.com/"]
  domains  = ["example.com"]
  included = []
  excluded = []

  # What to pull out of a page. Aliases are the other names a field goes by,
  # which is what lets it be found on a site that calls it something else.
  # Leave xpath and css empty and let `scour job train` propose them.
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
