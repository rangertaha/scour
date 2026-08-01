---
title: export
description: Getting the records out, in formats chosen for being boring.
---

# export

<p class="lede">Package <code>export</code> writes extracted records somewhere
useful. A crawler that can only be queried through its own command line is a
dead end: the records are the product, and they belong in whatever the rest of a
pipeline reads.</p>

<figure>
<img src="{{ '/img/export.svg' | relative_url }}" alt="Extracted records are grouped by the domain they came from and written as one file per site, as CSV or JSON, or posted to a webhook.">
</figure>

## Writing them out

```
scour record ls vehicle --write csv
scour record ls vehicle --write json --to ./out
scour record ls vehicle --write csv --confidence 0.8
scour record ls vehicle --write webhook --to https://example.com/ingest
```

```
exports/vehicle/www.example.com/2026-03-14.csv

id,url,confidence,format,label,make,model,price,year
1,http://www.example.com/cars/1/,0.9100,html,valid,Ford,F-150,"$42,000",2026
2,http://www.example.com/cars/2/,0.7200,html,unlabelled,Ram,1500,"$39,500",2026
```

## The four decisions in that file

**Grouped by domain, one file per site.** An export is then diffable and
re-runnable: a site that changed shows up as a changed file rather than as a
diff across everything ever crawled.

**Columns are the union of every record's fields, not the first record's.** A
field only some pages carry still gets a column, rather than being dropped
because the first row happened not to have it.

**The verdict travels with the record.** An export is also how records get
corrected outside scour, so a file that lost the marks would be a file you could
not send back. The column is spelled `label` and holds `valid`, `invalid` or
`unlabelled`, which is what the HTTP API and MCP have always called it. The CLI
says verdict for the same field, and reconciling the two names is one of the
things [the API design]({{ '/server/api.html' | relative_url }}) is for.

**Re-running on the same day overwrites rather than accumulating.** The date in
the path is the unit of work, so a re-run is a correction rather than a
duplicate.

## The formats

| Name | Output |
| --- | --- |
| `csv` | Flat, one row per record, fields as columns |
| `json` | The same rows, nested, so a field named `id` cannot collide with the record's own |
| `webhook` | Posted in batches to a URL |

The formats are deliberately boring, because the point is to hand off rather
than to be interesting.

The webhook reports what it delivered before any failure, so a retry does not
double-deliver. That matters more than it looks: the [bus]({{ '/bus/' | relative_url }})
delivers at least once, so an exporter that could not say where it got to would
turn one duplicate delivery into a duplicated batch.

If the endpoint needs a bearer token, the flag names the environment variable
holding it rather than the token itself:

```
scour record ls vehicle --write webhook --to https://example.com/ingest \
  --token-env INGEST_TOKEN
```

That is the same rule the [cache]({{ '/cache/' | relative_url }}) drivers
follow. scour takes no secrets in its configuration, so the file that says what
to crawl is never the file that has to be kept unreadable.

## Not to be confused with `scour job export`

`scour job export` is a different job, and the other half of `scour job import`: it
writes the domains and urls an item was built from, so a target list assembled
over a long crawl can be moved between databases or kept under version control.

```
scour job export vehicle --domains domains.txt --urls urls.txt
scour job import other   --domains domains.txt --urls urls.txt
```

A domain that covers its subdomains is written `*.example.com`, which is how
import reads it back, so a round trip does not quietly narrow the target.

## Writing one

```go
func init() {
    export.Register("parquet", func(cfg export.Config) (export.Exporter, error) {
        return &parquetWriter{dir: cfg.Dir}, nil
    })
}
```

Selected by `scour record ls --write <name>`. See
[extending it]({{ '/architecture/extending.html' | relative_url }}).

<div class="pager" markdown="1">
<span markdown="1">&larr; [store]({{ '/store/' | relative_url }})</span>
<span markdown="1">[bus &amp; service]({{ '/bus/' | relative_url }}) &rarr;</span>
</div>
