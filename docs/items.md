# Shapes, entities, measurements

*Chapter seven of [the scour book](index.md).*

A job declares what it is looking for. One declaration has three lives: a
shape a spider extracts against, a set of assertions about things in the
world, and a measurement when it flows.

```mermaid
flowchart TB
  ITEM["item price<br/>of = company, time = observed<br/>property value, property observed"]

  ITEM --> REC["a record<br/>named properties, some nested,<br/>as somebody asked for them"]
  ITEM --> ENT["assertions<br/>this page said this company<br/>has this price, at this time"]
  ITEM --> MEAS["a measurement<br/>name, tags, fields, time"]

  REC --> F["json, csv, sqlite"]
  ENT --> G[("the entity graph")]
  MEAS --> S["nats, and the archive"]

  MEAS -. "tags are of and the relations, because an entity reference is<br/>bounded by definition; fields are everything else" .-> S
```

<details>
<summary>What this diagram shows</summary>

One item declaration becomes three things: a record with named properties,
edges to entities in a shared store, and a measurement whose tags are the
entity references and whose fields are the remaining properties.

</details>

*One model, three renderings. Nothing in a document says "this is an event";
what makes it one is that the properties split cleanly into things you group
by and things you measure.*

## Properties, and what a type buys

A property says what to look for, what shape the answer has, and how to clean
it up. Aliases, regexes, XPath and CSS are all evidence about where a value
lives; transforms are what to do with it once found.

```hcl
property "title" {
  type       = str
  required   = true
  aliases    = ["headline", "og:title"]
  transforms = [text, trim]
  examples   = ["Hello World"]
}
```

`examples` are evidence about how to find a value, not about what is being
extracted, so adding one does not change the item's fingerprint and does not
force a re-extraction of records that are still correct. Changing `type` does.

## An entity is a thing, not a string

A byline is text. The author is a person, and the same person appears in a
thousand articles under four spellings. Declaring `type = entity` says the
extracted text refers to something that exists independently of this page:

```hcl
property "author" {
  type   = entity
  entity = "person"
}

relation "publisher" {
  entity   = "company"
  property = self.domain
  topic    = ["climate@7"]
}
```

A property is extracted from the page. A relation is not: the publisher is the
site, so `self.domain` says where the value comes from instead. `self.` is a
predeclared vocabulary, so a misspelling is a parse error rather than an empty
value.

Everything the store keeps is an assertion with provenance: who said it, from
which page, under which item fingerprint, when. That is what makes a wrong
entity correctable rather than a fact that has quietly become part of the
data. It is shared across jobs and sits behind a service, because the whole
value of an entity store is that two jobs crawling different sites agree about
who Acme is.

> **The failure mode to design against**
>
> An entity store fed by extraction, feeding extraction, is a loop that can
> teach itself something wrong and then keep confirming it. Provenance is
> what makes it recoverable: every assertion knows which page and which run
> produced it, so a bad run can be retracted rather than argued with.

## Tags, fields, time

| Part | Comes from |
| --- | --- |
| Measurement | The item's name |
| Tags | `of`, the relations, and any property declared `tag` |
| Fields | Everything else |
| Time | The property `time` names |

**Tag cardinality is the failure mode.** Every distinct tag value is another
series, so tagging by URL or by headline destroys a time-series store. Entity
references are safe because entities are bounded by definition, which is why
they are the tags and free text never is. A scalar that genuinely is a
dimension says `tag = true`; one that cannot be a single value is refused.

**Time is event time, never ingest time.** A headline published at nine and
crawled at half eleven is an event at nine. Getting that wrong makes replay
and backfill produce series that are wrong in a way nobody notices for months.

**Content does not travel.** A five thousand character body is not a field. A
headline event carries its tags, its small fields and a reference to the body,
which is the rule the bus already follows for pages.

## Two shapes, one mechanism

A headline happens once. A price is the same thing measured again. The
difference shows up in the subject, and `of` is what puts it there:

```text
events.news.headline                 every headline
events.markets.price.<company>       one subject per company
```

Which is what lets a consumer subscribe to one company rather than filter the
firehose, and what makes the latest value a fetch rather than a scan.

---

[Back: What to fetch next](frontier.md) · [Next: A graph, not a list](pipeline.md)
