---
title: cache
description: Fetched page bodies, why they are the one part of scour's state that need not be local, and the drivers that follow from that.
---

# cache

<p class="lede">Package <code>cache</code> stores fetched page bodies so a
re-crawl, and every later pass that needs the bytes again, does not
re-download them.</p>

<figure>
<img src="{{ '/img/cache.svg' | relative_url }}" alt="On one machine a directory holds the fetched bodies and the trainer reads it back. With crawlers on several machines each would write to its own disk, so they are pointed at one bucket instead.">
</figure>

## The split it enforces

The database records that a page was fetched and what its key is. The body
itself is kept separately, keyed by a hash of the URL.

That split is what makes training cheap to repeat. Induction reads bytes a
previous crawl already paid for, so retraining after every correction costs CPU
and no network, and a change to inference can be measured against a fixed corpus
rather than against a web that moved underneath the comparison. Every number on
the [results]({{ '/results/' | relative_url }}) page depends on that property.

It is also what makes a corpus budgetable. Training is linear in bytes rather
than in pages, so the cache reports its size directly:

```
scour status news2
cache       2,829 pages, 580.2MB
```

## Why the driver is an extension point

On one machine a directory is right. With crawlers on several it is wrong: each
writes to its own disk, and the trainer reads an empty cache with a database
full of keys pointing at nothing.

That is not a hypothetical. It is the failure that makes distribution real, and
the reason `cache.Store` is an interface rather than a path.

```toml
[cache]
driver = "s3"
url    = "s3://my-bucket/pages"

[cache.options]
region = "us-east-1"
```

```toml
[cache]
driver = "gcs"
url    = "gs://my-bucket/pages"
```

| Driver | Where the bytes go | Build |
| --- | --- | --- |
| `local` | A directory. The default | Always |
| `s3` | An S3 bucket | `-tags cloud` |
| `gcs` | A Google Cloud Storage bucket | `-tags cloud` |

Credentials are the provider's own: `AWS_*` and the shared config for S3,
application default credentials for Google. scour takes no keys in its
configuration, so a crawler needs no secrets in the file that also says what to
crawl.

## The build tag

Linking the AWS and Google SDKs adds 29MB to the stripped binary, and most
crawls keep their pages in a directory:

```
make build-cloud        # or: go build -tags cloud ./cmd/scour
```

A build without them still registers the names, and says the driver needs a
different build rather than pretending it does not exist. That is the same
principle as the not-yet-written registry entries elsewhere: a name that exists
should never be reported as a typo.

## What is safe to delete

The cache is the only part of scour's state you can throw away and rebuild by
crawling again. Everything in the [store]({{ '/store/' | relative_url }}) is
state you would have to re-crawl to reconstruct, and the marks in it could not
be reconstructed at all.

| Path | Contents |
| --- | --- |
| `~/.cache/scour/pages/<domain>/` | Per-user install |
| `/var/cache/scour/pages/<domain>/` | Packaged install |

<div class="pager" markdown="1">
<span markdown="1">&larr; [schedule]({{ '/schedule/' | relative_url }})</span>
<span markdown="1">[parse &amp; wom]({{ '/parse/' | relative_url }}) &rarr;</span>
</div>
