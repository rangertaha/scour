// SPDX-License-Identifier: GPL-3.0-or-later

package store

import "time"

// Entity is the kind of thing a crawl is looking for. Everything else in the
// schema hangs off one.
type Entity struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"uniqueIndex;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Aliases      []Alias       `gorm:"constraint:OnDelete:CASCADE"`
	Properties   []Property    `gorm:"constraint:OnDelete:CASCADE"`
	Targets      []Target      `gorm:"constraint:OnDelete:CASCADE"`
	ContentTypes []ContentType `gorm:"constraint:OnDelete:CASCADE"`
}

// Alias is another word a page might use for an entity, so a page that never
// uses the entity's own name can still match.
type Alias struct {
	ID       uint   `gorm:"primaryKey"`
	EntityID uint   `gorm:"uniqueIndex:idx_alias_entity_word;not null"`
	Word     string `gorm:"uniqueIndex:idx_alias_entity_word;not null"`
}

// Property is an attribute an entity should have. Example is a sample value,
// which is what lets induction recognise other values of the same kind.
type Property struct {
	ID       uint `gorm:"primaryKey"`
	EntityID uint `gorm:"uniqueIndex:idx_prop_entity_name;not null"`
	// Domain scopes the property to one site. Empty is the entity's default,
	// which every site starts from.
	//
	// A schema describes what is wanted; a site describes how it says it. Those
	// are not the same thing, and one example cannot serve both: teaching that
	// the byline on one paper reads "Hannah McLeod" says nothing about the next
	// paper, and stored entity-wide it would overwrite what the last site
	// taught. So a taught example belongs to the site it was taught on.
	Domain  string `gorm:"uniqueIndex:idx_prop_entity_name;default:''"`
	Name    string `gorm:"uniqueIndex:idx_prop_entity_name;not null"`
	Type    string
	Example string
	// Regex says what an acceptable value looks like, and optionally where in it
	// the value is.
	//
	// One pattern does both jobs because a regex already answers both
	// questions. Text it rejects is not this property, whatever else about it
	// agrees, so it decides which node wins. Capture group one is the value, so
	// it also decides what that node yields. With no capture group it only
	// validates, and extracting without validating is incoherent: a pattern
	// that does not match has nothing to extract.
	//
	// Both halves are needed on real pages. Validation moves the choice to a
	// better node, which is how a Facebook URL stops being a byline. Extraction
	// reaches values no node holds cleanly: an author URL is the right node with
	// the name inside it, and an alt text reading "Author:THE NEWSROOM" carries
	// its own label with no second node holding the name alone.
	Regex string
	// Label is what the name attached to the value must look like: the
	// attribute, key, class or neighbouring text saying what the value is.
	//
	// Aliases list the words a page might use, which is easy to write and
	// imprecise. A pattern says exactly which count. Substring matching finds
	// "title" inside "subtitle" and "titlebar"; ^(og:|twitter:)?title$ does
	// not. And terms change by language while standards do not, so
	// titulo|título|заголовок|title says so without a stemmer.
	Label string
	// Description says what the field means, in words a page might also use.
	// The matcher scores description overlap, so this is not documentation:
	// it is training data.
	Description string
	// Aliases are the other words a page might label this field with. A page
	// saying "Manufacturer" where the schema says "make" is the ordinary case,
	// not the exception.
	Aliases []PropertyAlias `gorm:"constraint:OnDelete:CASCADE"`
}

// PropertyAlias is another word a page might use to label a property.
//
// It is a row rather than a delimited column because an alias is frequently a
// phrase: "pickup truck", "model year", "asking price". Any delimiter would
// eventually split one of them in half.
type PropertyAlias struct {
	ID         uint   `gorm:"primaryKey"`
	PropertyID uint   `gorm:"uniqueIndex:idx_palias_prop_word;not null"`
	Word       string `gorm:"uniqueIndex:idx_palias_prop_word;not null"`
}

// TargetKind distinguishes a whole site from a single page.
type TargetKind string

// The kinds of crawl target.
const (
	TargetDomain TargetKind = "domain"
	TargetURL    TargetKind = "url"
)

// Target is where a crawl starts.
type Target struct {
	ID         uint       `gorm:"primaryKey"`
	EntityID   uint       `gorm:"uniqueIndex:idx_target_entity_value;not null"`
	Kind       TargetKind `gorm:"uniqueIndex:idx_target_entity_value;not null"`
	Value      string     `gorm:"uniqueIndex:idx_target_entity_value;not null"`
	Subdomains bool
	Depth      int
}

// ContentType restricts an entity's crawls to particular formats. An entity
// with none configured uses the crawl defaults.
type ContentType struct {
	ID       uint   `gorm:"primaryKey"`
	EntityID uint   `gorm:"uniqueIndex:idx_ctype_entity_type;not null"`
	Type     string `gorm:"uniqueIndex:idx_ctype_entity_type;not null"`
}

// Host carries per-host crawl policy, either configured or learned. It is
// shared across entities, because politeness is owed to the server rather than
// to any one crawl.
type Host struct {
	ID          uint   `gorm:"primaryKey"`
	Host        string `gorm:"uniqueIndex;not null"`
	Rate        time.Duration
	Concurrency int
	Robots      bool
	Transport   string
}

// URLStatus is where a URL has got to in the crawl.
type URLStatus string

// The states a URL moves through.
const (
	URLQueued  URLStatus = "queued"
	URLFetched URLStatus = "fetched"
	URLFailed  URLStatus = "failed"
	URLSkipped URLStatus = "skipped"
)

// URL is one entry in the frontier. Hash is the normalised URL's digest and
// carries the uniqueness constraint, so re-discovering a URL is an upsert.
//
// ParentID and Role are what the crawl chain needs: the parent edge
// reconstructs the path for decoding, and the role is the decoded state.
type URL struct {
	ID          uint   `gorm:"primaryKey"`
	EntityID    uint   `gorm:"index;not null"`
	Hash        string `gorm:"uniqueIndex;not null"`
	URL         string `gorm:"not null"`
	ParentID    *uint  `gorm:"index"`
	Depth       int
	Score       float64 `gorm:"index"`
	Role        string
	Status      URLStatus `gorm:"index"`
	StatusCode  int
	ContentType string
	Size        int64
	Latency     time.Duration
	Matches     int
	FetchedAt   *time.Time
	NextAt      *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Response records one fetch. The body itself lives in the page cache, keyed
// by CacheKey, so the database stays small.
type Response struct {
	ID        uint `gorm:"primaryKey"`
	URLID     uint `gorm:"index;not null"`
	Status    int
	Headers   string
	CacheKey  string
	FetchedAt time.Time
}

// Rule is one induced locator, flattened from the nested form induction
// produces. ParentID links a field rule to the container rule it is addressed
// relative to.
type Rule struct {
	ID          uint  `gorm:"primaryKey"`
	EntityID    uint  `gorm:"index;not null"`
	ParentID    *uint `gorm:"index"`
	Prop        string
	XPath       string
	Selector    string
	Path        string
	Regex       string
	URIPattern  string
	Probability float64
	Support     int
	CreatedAt   time.Time
}

// Label is the verdict a person passed on a record.
type Label string

// The verdicts a record can carry.
const (
	Unlabelled Label = "unlabelled"
	Valid      Label = "valid"
	Invalid    Label = "invalid"
)

// Record is one extracted entity instance. Fingerprint is derived from the
// values, so re-extracting the same record from the same page is an upsert
// rather than a duplicate.
type Record struct {
	ID          uint   `gorm:"primaryKey"`
	EntityID    uint   `gorm:"index;not null"`
	URLID       uint   `gorm:"index"`
	Fingerprint string `gorm:"uniqueIndex;not null"`
	Confidence  float64
	Format      string
	Label       Label `gorm:"index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time

	Values []Value `gorm:"constraint:OnDelete:CASCADE"`
}

// Value is one property of one record.
type Value struct {
	ID       uint   `gorm:"primaryKey"`
	RecordID uint   `gorm:"uniqueIndex:idx_value_record_prop;not null"`
	Prop     string `gorm:"uniqueIndex:idx_value_record_prop;not null"`
	Text     string
}

// ModelMeta describes the scoring model on disk for one entity.
type ModelMeta struct {
	ID           uint `gorm:"primaryKey"`
	EntityID     uint `gorm:"uniqueIndex;not null"`
	Path         string
	Algorithm    string
	Accuracy     float64
	Observations int
	TrainedAt    time.Time
}

// ChainKind distinguishes the two sequence models.
type ChainKind string

// The chains scour fits.
const (
	// ChainExtract orders fields within a record. It transfers between sites
	// and entities, so it is stored with a null EntityID.
	ChainExtract ChainKind = "extract"
	// ChainCrawl orders page roles along a crawl path. It is per entity.
	ChainCrawl ChainKind = "crawl"
)

// Chain is a fitted hidden Markov chain. Transitions is JSON, since its shape
// belongs to the model rather than to the database.
type Chain struct {
	ID           uint      `gorm:"primaryKey"`
	EntityID     *uint     `gorm:"index"`
	Kind         ChainKind `gorm:"index;not null"`
	States       string
	Transitions  string
	Observations int
	FittedAt     time.Time
}

// Visit is colly's visited set, kept per entity so two entities crawling the
// same site do not mask each other's work. RequestID is colly's own URL hash,
// which is what its revisit check looks up.
//
// The hash is a uint64 in colly and an int64 here, holding the same bits.
// database/sql refuses to bind a uint64 with the high bit set, and since
// colly's hash is FNV-64 that is half of all URLs; binding them as uint64
// makes the visited check fail for those, and colly quietly drops every
// request whose check errors. The values are only ever compared for equality,
// so reinterpreting the bits is lossless.
type Visit struct {
	ID        uint  `gorm:"primaryKey"`
	EntityID  uint  `gorm:"uniqueIndex:idx_visit_entity_request;not null"`
	RequestID int64 `gorm:"uniqueIndex:idx_visit_entity_request;not null"`
	VisitedAt time.Time
}

// Cookie is colly's cookie jar, one row per host, so a session survives a
// restart rather than making the crawler log in again.
type Cookie struct {
	ID        uint   `gorm:"primaryKey"`
	EntityID  uint   `gorm:"uniqueIndex:idx_cookie_entity_host;not null"`
	Host      string `gorm:"uniqueIndex:idx_cookie_entity_host;not null"`
	Value     string
	UpdatedAt time.Time
}

// QueueItem is one pending request, holding colly's serialised form so the
// queue can be handed back exactly what colly put in it.
//
// Score is what the queue is ordered by from M4 onwards. Until a model exists
// every score is equal, so the tie-break on ID keeps the order the crawl would
// have had anyway.
type QueueItem struct {
	ID       uint    `gorm:"primaryKey"`
	EntityID uint    `gorm:"index:idx_queue_entity_score;not null"`
	Score    float64 `gorm:"index:idx_queue_entity_score"`
	// Hash identifies the URL, so the item can be released when the fetch is
	// recorded without the releaser having to know the queue's row ids.
	Hash string `gorm:"index"`
	// Host is who the fetch will be asked of. Politeness is owed to a server
	// rather than to a crawl, so it has to be visible where work is handed out:
	// several crawlers each obeying a per-process rate limit still add up to
	// several times the load on one site.
	Host string `gorm:"index"`
	Data []byte `gorm:"not null"`
	// LeasedUntil is when a handed-out item returns to the queue if nothing has
	// reported back. Nil means it is waiting rather than in flight.
	//
	// Handing an item out used to delete it, so a crawler that died between
	// taking a URL and fetching it lost that URL with no trace. A lease is what
	// makes the loss recoverable, and it is the same mechanism a second crawler
	// needs to be handed work safely.
	LeasedUntil *time.Time `gorm:"index"`
	// Attempts is how many times the item has been handed out. Not every
	// hand-out ends in a fetch: colly declines a request it has already
	// visited or that exceeds the depth, and a declined request reports no
	// outcome, so nothing releases it. Without a limit it would be re-offered
	// on every expiry and declined again forever.
	Attempts  int
	CreatedAt time.Time
}

// PageRole is the role decoding gave a page.
//
// It lives apart from the frontier on purpose. Roles are learned state, like a
// model: resetting a crawl means fetching the site again, not forgetting what
// its shape is. Keeping them on the URL row would delete them with it.
type PageRole struct {
	ID        uint   `gorm:"primaryKey"`
	EntityID  uint   `gorm:"uniqueIndex:idx_role_entity_hash;not null"`
	Hash      string `gorm:"uniqueIndex:idx_role_entity_hash;not null"`
	URL       string `gorm:"not null"`
	Role      string `gorm:"index"`
	DecodedAt time.Time
}

// Judgement is one model's verdict on one question, kept so it is paid for
// once.
//
// It is keyed by a hash of the question rather than by any page or entity,
// because the same question recurs across pages, across entities and across
// retrains. It is a cache: deleting the table costs money, not correctness.
type Judgement struct {
	ID    uint    `gorm:"primaryKey"`
	Key   string  `gorm:"uniqueIndex;not null"`
	Model string  `gorm:"index"`
	Score float64 `gorm:"not null"`
	// Verdict is the answer when the question was a choice between named
	// categories rather than a score. Only one of the two is ever set.
	Verdict string
	// Uses counts how often the cached answer was reused, which is the only
	// honest way to report what caching saved.
	Uses      int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// tables lists every model, in dependency order, for migration.
func tables() []any {
	return []any{
		&Entity{}, &Alias{}, &Property{}, &PropertyAlias{}, &Target{}, &ContentType{},
		&Host{}, &URL{}, &Response{},
		&Rule{}, &Record{}, &Value{},
		&ModelMeta{}, &Chain{}, &Judgement{},
		&Visit{}, &Cookie{}, &QueueItem{}, &PageRole{},
	}
}
