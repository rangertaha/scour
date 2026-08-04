# A shop crawler.
#
# Prices are the awkward field: they arrive as "£12.99", "12,99 EUR" and
# "From $9". Left as a string here, because a number that silently dropped the
# currency would be worse than the text it came from. Give it a regex once you
# have seen what the site actually writes.

job {{.Name | quote}} {
  start    = ["https://shop.example/"]
  domains  = ["shop.example"]
  included = ["*/product/*", "*/p/*"]
  excluded = ["*/cart*", "*/checkout*", "*/account*"]

  item "product" {
    description = "One thing for sale"

    property "url" {
      type       = str
      required   = true
      aliases    = ["canonical", "link"]
      transforms = [absurl]
    }

    property "name" {
      type       = str
      required   = true
      aliases    = ["title", "og:title", "productName"]
      transforms = [text, trim]
    }

    property "sku" {
      type    = str
      aliases = ["mpn", "gtin", "productID", "item_number"]
    }

    property "brand" {
      type       = str
      aliases    = ["manufacturer", "vendor"]
      transforms = [text, trim]
    }

    property "price" {
      type       = str
      required   = true
      aliases    = ["offers.price", "amount"]
      transforms = [text, trim]
    }

    property "currency" {
      type    = str
      aliases = ["priceCurrency", "offers.priceCurrency"]
    }

    property "availability" {
      type    = str
      aliases = ["stock", "offers.availability", "inStock"]
    }

    property "description" {
      type       = str
      aliases    = ["og:description", "summary"]
      transforms = [text, normalise_space]
    }

    property "image" {
      type       = url
      aliases    = ["og:image", "thumbnail"]
      transforms = [absurl]
    }
  }

  scheduler {
    policy      = "priority"
    rate        = "3s"
    concurrency = 1
    max_depth   = 5
    max_pages   = 20000
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
    step "clean" "product" {}

    step "validate" "product" {
      requires = [clean.product]
    }
  }

  exporter "csv" "product" {
    dir = "./out"
  }
}
