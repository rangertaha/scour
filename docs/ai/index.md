---
title: ai
description: Access to language models, kept behind an interface with one method, and bounded by a cache and a budget.
---

# ai

<p class="lede">Package <code>ai</code> is scour's access to language models. It
deliberately knows nothing about crawling or extraction.</p>

<figure>
<img src="{{ '/img/ai.svg' | relative_url }}" alt="The parts that want a model's judgement ask through a cache keyed by the question, which reaches a provider behind a one-method interface. A local model and a hosted one are the same thing to the rest of scour.">
</figure>

## One question

A provider answers exactly one thing: *given this prompt, return JSON matching
this schema.* Everything that wants a model's judgement builds on that.

```go
type Provider interface {
    Name() string
    JSON(ctx context.Context, req Request) ([]byte, error)
}
```

Two decisions in that signature are worth stating, because both are about
keeping the seam from widening.

**A schema is required, not optional.** A caller that wanted prose would be
asking the wrong package. Requiring a schema is what makes the answer something
the rest of scour can act on rather than something it has to parse hopefully.

**The raw bytes come back rather than a decoded value.** The caller owns the
shape it expects, so a provider needs no change when a new kind of question is
asked. A `Provider` written today keeps working when the matcher starts asking
about something it has never heard of.

The system prompt is separate from the prompt in `Request`, because providers
cache it and because it is the part that does not vary between calls.

## Who asks

| Caller | Question | Turned on by |
| --- | --- | --- |
| [matcher]({{ '/matcher/' | relative_url }}) | is this node that field? | `[model] matcher = "llm"` |
| [classify]({{ '/classify/' | relative_url }}) | what is this page about? | `[model] classifier = "llm"` |

Both are off by default. The heuristic matcher needs no network, no key and no
training data, and everything richer is measured against it.

## What bounds it

A model on the extraction path is asked about a number of candidates that runs
into the millions on a real crawl, so three things sit between the caller and
the provider.

**A cache, keyed by the question.** The `judgements` table stores what was
asked and what came back. The same question across pages, items and retrains is
paid for once, which is what makes retraining after a correction affordable when
the matcher is a hosted model. It is keyed by the question rather than by the
item for exactly that reason: a second item over the same sites inherits the
answers.

**A floor.** Candidates the cheap pass has already settled are never asked
about, so the model sees only the ones that were genuinely uncertain.

**A budget.** `[model] budget` caps calls per training run. Spending it returns
an error rather than quietly continuing without judgement, so a run that ran out
says so instead of producing a quietly worse model.

## Configuring a provider

One `[[ai]]` block per model, referred to by name from `[model]`:

```toml
[model]
matcher = "llm"
ai      = "local"

[[ai]]
name     = "local"
provider = "ollama"
model    = "gemma3:270m"
endpoint = "http://localhost:11434"

[[ai]]
name        = "claude"
provider    = "anthropic"
model       = "claude-opus-5"
effort      = "low"
api_key_env = "ANTHROPIC_API_KEY"
```

| Key | Means |
| --- | --- |
| `name` | What `[model]` refers to this block by |
| `provider` | `ollama` or `anthropic` |
| `model` | The provider's own model identifier |
| `effort` | A provider-specific hint for how hard to think |
| `endpoint` | Where the provider is reached, which is how a local model on another machine is used |
| `api_key_env` | The environment variable holding the credential |
| `timeout` | Bounds a single call |

**The key is named, never written.** `api_key_env` holds the name of an
environment variable, so a config file stays safe to commit and a crawler needs
no secrets in the file that also says what to crawl. That is the same rule the
[cache]({{ '/cache/' | relative_url }}) drivers and the
[webhook exporter]({{ '/export/' | relative_url }}) follow.

## Why it is a seam at all

The expensive, unpredictable part of a system is the part most worth being able
to replace and most worth being able to test without. Keeping the interface to
one method is what lets a local model and a hosted one be the same thing to
everything else, and what lets the whole of extraction be exercised against a
stub.

It is also why `Embedder` is a separate interface rather than another method on
`Provider`: most providers do one or the other well, and a caller that needs
embeddings should fail to find them rather than receive something improvised.

<div class="pager" markdown="1">
<span markdown="1">&larr; [score]({{ '/score/' | relative_url }})</span>
<span markdown="1">[The algorithms]({{ '/algorithms/' | relative_url }}) &rarr;</span>
</div>
