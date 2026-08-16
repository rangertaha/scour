---
title: Chains run both ways
description: Middleware wraps a stage, so every link sees the request going out and the response coming back.
---

# Chains run both ways

*Chapter three of [the scour book](../).*

A chain wraps its stage rather than hooking it, so every link sees the request
on the way out and the response on the way back, in opposite orders. That is
what makes `order` mean something, and it is the part that is easy to get
wrong.

<figure>
<img src="{{ '/img/chains.svg' | relative_url }}" alt="The same three links, twice. On the way out a request passes offsite at 500, retry at 550 and cache at 900, in that order, and reaches the network. On the way back the response passes the same three in reverse. A cache hit returns from 900 without the network being reached, so the links outside it still see a response and the ones inside it never ran. A drop at offsite ends the request there.">
<figcaption>The same three links, seen twice. On the way out they run low to high; on the way back, high to low. A cache hit returns without calling the rest, so the links outside it still see a response and the links inside it never ran.</figcaption>
</figure>

The numbers are Scrapy's, because copying a known-good ordering is cheaper
than rediscovering it, and the reasoning transfers with them. `cache` at 900
is the last thing before the network, so a hit short-circuits the fetch only
after every other request middleware has had its say: a URL the offsite rule
would drop is dropped whether or not it happens to be cached.

## Two things every link may do

**Short-circuit:** return a result without calling the rest. A cache hit is
this and nothing more.

**Drop:** return `ErrDrop`. Refusing a URL out of scope is this. It is a
sentinel rather than an ordinary error because a dropped request is a normal
outcome of a working crawl, and counting it as a failure would make every
politely behaved crawl look broken.

Both are in the contract from the start because neither can be added to it
later without changing every link ever written.

They also fall out of wrapping rather than needing anything added. The
alternative shape, a pair of `Request` and `Response` methods, is worse in
three ways: it needs a convention for a link that wants to short-circuit, it
makes a link that needs state across the two directions stash it somewhere,
and it cannot express "run this on the way back even though the way out
failed", which is what a timer and a stats counter both want.

```go
func timing(next Handler) Handler {
    return HandlerFunc(func(ctx context.Context, req *Request) (*Response, error) {
        started := time.Now()               // on the way out
        resp, err := next.Handle(ctx, req)
        log.Println(time.Since(started))    // on the way back
        return resp, err
    })
}
```

## From a list of names to a chain that runs

The job document can say a job wants a plugin called `cache` at 900. The chain
machinery can run an ordered set of middleware. Neither of them can answer
whether `cache` is a thing that exists.

<figure>
<img src="{{ '/img/chains-2.svg' | relative_url }}" alt="Plugin names from the job document are resolved against a registry of what this node has compiled in. Names that resolve become ordered links wrapping the core; a name nothing implements refuses the whole chain.">
<figcaption>The seam. It is the first place a job naming a plugin nothing implements is refused, and it refuses the whole chain rather than running a partial one.</figcaption>
</figure>

Every missing name is reported at once, along with what the node does have. A
job loading six plugins on a node with four of them should be told which two,
not sent round the loop twice.

The chain that comes back owns whatever its plugins opened. A cache plugin
holding a bucket has nowhere to put a `Close`, because what a plugin hands
back is a function and a function has nowhere to keep a method; so it
registers one with the chain, and the chain closes them last opened first when
the job stops. A chain refused halfway closes what it had already opened
before it returns, because the caller has no chain to close it with.

## Where middleware conventionally sits

A catalogue of positions, not a list of working parts. A name in it is a claim
about where something would go if it existed. What exists is what a registry
says exists, and that is asked when a chain is built.

| Order | Downloader | Order | Spider |
| --- | --- | --- | --- |
| 500 | `offsite` | 50 | `httperror` |
| 520 | `contenttype` | 300 | `topic` |
| 543 | `cookies` | 500 | `offsite` |
| 544 | `auth` | 700 | `referer` |
| 550 | `retry` | 800 | `urllength` |
| 560 | `headers` | 900 | `depth` |
| 580 | `metarefresh` |  |  |
| 610 | `proxy` |  |  |
| 850 | `stats` |  |  |
| 900 | `cache` |  |  |

> **Not in the table**
>
> Decoding, robots and redirects. Each of them has exactly one correct
> position, and a position that can be configured is a position that can be
> configured wrongly. The next chapter is about what that means for a
> request.

---

[Back: One document, everything in it](../job/) · [Next: Fetching, politely](../downloader/)
