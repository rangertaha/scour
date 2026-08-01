---
title: transport
description: How a request reaches the network, why the browser is a transport, and how escalation is learned per host.
---

# transport

<p class="lede">Package <code>transport</code> is how a request actually reaches
the network. The extension point is <code>http.RoundTripper</code> rather than an
interface of scour's own, because that is what colly already accepts: a plugin
that satisfies the standard library satisfies scour.</p>

<figure>
<img src="{{ '/img/transport.svg' | relative_url }}" alt="A request goes to plain HTTP unless the host is already known to need a browser. A response that looks unrendered escalates to the webdriver transport, and the host is remembered so the wasted HTTP attempt is skipped next time.">
</figure>

## Why this seam is where it is

Some sites send an empty shell and build the page in JavaScript. Plain HTTP sees
no content and no links, so a crawl stops at the front door.

The obvious fix is a second code path: detect those sites, render them, and
handle rendered pages separately. That fix is wrong, and the reason is worth
stating because it generalises. A second path means every downstream part has to
know which one it is on: the cache would key rendered pages differently, the
scorer would see two sorts of response, training would have to decide whether a
rendered page counts.

Putting rendering below everything else costs one interface and buys the
opposite property. A rendered page is cached, scored, trained on and searched
exactly like any other, and nothing downstream can tell the difference.

## The policy

```
scour start vehicle --browser auto
```

| `--browser` | What happens |
| --- | --- |
| `never` | Plain HTTP only |
| `auto` | HTTP first, browser only when the response looks unrendered (default) |
| `always` | Skip the HTTP attempt, render everything |

`auto` is the default because most of the web needs no browser and rendering
every page would be slow and expensive.

## What counts as unrendered

Three conditions, all of which must hold together:

1. the response is HTML,
2. it carries scripts,
3. and yet it has no links and almost no text.

Any one of those alone is normal. A page with no links might be a leaf; a page
with scripts is most pages; a short page is a short page. Requiring all three is
what keeps the test from escalating half the web into a browser tab.

Once a host proves it needs a browser, scour remembers and stops paying for the
HTTP attempt it is going to discard. A host can also be pinned by hand, which is
only worth doing to skip the first discovery:

```toml
[[host]]
host      = "app.example.com"
transport = "webdriver"
```

## The cost, and the fallback

```toml
[browser]
enabled   = true
policy    = "auto"
pool      = 2        # tabs rendering at once
timeout   = "45s"    # per render, not per request
exec_path = ""       # browser binary; empty means find one
```

The tab pool is deliberately small, because a browser tab costs far more than a
socket. Rendering requires Chrome or Chromium on the machine; without one,
`auto` degrades to plain HTTP. If the browser cannot start mid-crawl, the crawl
keeps the plain response and continues rather than failing, on the grounds that
a partial page is worth more than a dead crawl.

## Writing one

Anything satisfying `http.RoundTripper` will do, which includes most existing Go
proxy and instrumentation middleware unchanged:

```go
func init() {
    transport.Register("myproxy", func(cfg transport.Config) (http.RoundTripper, error) {
        return &myProxy{timeout: cfg.Timeout}, nil
    })
}
```

Selected per host with `[[host]] transport`. See
[extending it]({{ '/architecture/extending.html' | relative_url }}) for the
registry the name is looked up in.

<div class="pager" markdown="1">
<span markdown="1">&larr; [crawl]({{ '/crawl/' | relative_url }})</span>
<span markdown="1">[schedule]({{ '/schedule/' | relative_url }}) &rarr;</span>
</div>
