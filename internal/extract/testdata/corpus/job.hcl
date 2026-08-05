# The job the corpus is measured with.
#
# It is `internal/templates/files/news.hcl`, the job scour hands somebody who
# asks for a news crawler, with two things added: the class names two content
# management systems put an article's text in, and the regex for a date that is
# only ever written out for people to read. Both are what a person would write
# after looking at a few pages.
#
# Deliberately not tuned to these fifteen pages. A job with a selector per page
# would report a hundred per cent and would be measuring the corpus rather than
# extraction.

job "corpus" {
  start    = ["https://corpus.example/"]
  domains  = ["corpus.example"]
  included = ["corpus.example"]

  item "article" {
    description = "One story, as fifteen different sites variously spell one"

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

      # The object answers to `byline` as well, which the template does not do.
      # Without it the only thing that can find an author is a meta element, and
      # a meta element is not a node, so the fields below would never be looked
      # for at all. That is a real trap and the corpus should measure a job that
      # has avoided it rather than one that has not.
      aliases = ["byline"]

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

      # For the sites that print the date and nowhere say it in markup. Third of
      # the four ways, so a site that does mark its dates up is still read from
      # the markup rather than from whatever the prose happens to mention.
      regexes = ["[0-9]{1,2} (?:January|February|March|April|May|June|July|August|September|October|November|December) [0-9]{4}"]
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

      # The two class names that carry an article's text on a large share of the
      # web, because two content management systems ship them. Taught, so they
      # beat the guess, and so a page that has one is measured as a page with a
      # locator rather than as a lucky semantic match.
      css = [".article-body", ".entry-content"]
    }
  }
}
