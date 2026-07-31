# scour

BIN        := scour
PKG        := github.com/rangertaha/scour
CMD        := ./cmd/scour
BUILD_DIR  := bin
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X $(PKG)/internal/version.version=$(VERSION)
GO         ?= go

OWN_PKGS := $(shell $(GO) list ./...)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: all
all: fmt-check vet lint test build ## Run every check, then build

.PHONY: build
build: ## Build the binary into bin/
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BIN) $(CMD)

.PHONY: build-cloud
build-cloud: ## Build with the S3 and GCS page stores included
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -tags cloud -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BIN) $(CMD)

.PHONY: install
install: ## Install the binary into GOBIN
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' $(CMD)

.PHONY: run
run: build ## Build and run, e.g. make run ARGS="list"
	$(BUILD_DIR)/$(BIN) $(ARGS)

.PHONY: test
test: ## Run the tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover: ## Run the tests and open the coverage report
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic $(OWN_PKGS)
	$(GO) tool cover -func=coverage.out | tail -1
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: bench
bench: ## Run the benchmarks
	$(GO) test -run '^$$' -bench . -benchmem $(OWN_PKGS)

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

.PHONY: fmt
fmt: ## Format the code
	gofmt -w $(shell find . -name '*.go')

.PHONY: fmt-check
fmt-check: ## Fail if anything needs formatting
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

.PHONY: clean
clean: ## Remove build and coverage artefacts
	rm -rf $(BUILD_DIR) coverage.out coverage.html dist

.PHONY: snapshot
snapshot: ## Build a local release snapshot with goreleaser
	goreleaser release --snapshot --clean

.PHONY: version
version: ## Print the version that would be stamped into a build
	@echo $(VERSION)

# ---------------------------------------------------------------------------
# News pipelines
#
# Two corpora, two shapes. A feed is the data: one fetch gives you a list of
# articles, so it is crawled at depth 1 and read as a feed. A news site is a
# doorway: the articles are somewhere behind the homepage, so it is crawled
# deeper and read as html.
#
# Every target is idempotent and every crawl is bounded, because the site list
# is large enough that an unbounded run is a mistake you only make once.
# ---------------------------------------------------------------------------

NEWS_DIR      ?= $(HOME)/Downloads/NEWS
FEED_LIST     ?= $(NEWS_DIR)/FEED.lst
SITE_LIST     ?= $(NEWS_DIR)/URLS.lst

FEED_ITEM   ?= news-feeds
SITE_ITEM   ?= news-sites

# Budgets. A crawl that hits one stops cleanly and leaves the frontier where it
# is, so the next run resumes rather than starting over.
FEED_PAGES    ?= 500
FEED_TIME     ?= 15m
SITE_PAGES    ?= 2000
SITE_TIME     ?= 30m
SITE_DEPTH    ?= 3

EXPORT_FORMAT ?= csv
SCOUR         := $(BUILD_DIR)/$(BIN)

.PHONY: news-feeds
news-feeds: news-feeds-import news-feeds-crawl news-feeds-train news-feeds-export ## Import, crawl, train and export the RSS feeds

.PHONY: news-feeds-import
news-feeds-import: build ## Load the feed list and give it an article schema
	@test -f '$(FEED_LIST)' || { echo "no feed list at $(FEED_LIST); set FEED_LIST="; exit 1; }
	$(SCOUR) add $(FEED_ITEM) --template article
	$(SCOUR) add $(FEED_ITEM) --type feed --type xml
	$(SCOUR) import $(FEED_ITEM) --urls '$(FEED_LIST)'

.PHONY: news-feeds-crawl
news-feeds-crawl: build ## Fetch the feeds, one level deep
	$(SCOUR) crawl $(FEED_ITEM) --depth 1 \
		--max-pages $(FEED_PAGES) --max-time $(FEED_TIME)

.PHONY: news-feeds-train
news-feeds-train: build ## Learn where an article's fields live inside a feed
	$(SCOUR) train $(FEED_ITEM)

.PHONY: news-feeds-export
news-feeds-export: build ## Write the articles out, one file per domain
	$(SCOUR) export $(FEED_ITEM) --format $(EXPORT_FORMAT)

.PHONY: news-articles
news-articles: news-articles-import news-articles-crawl news-articles-train news-articles-export ## Import, crawl, train and export the news sites

.PHONY: news-articles-import
news-articles-import: build ## Load the site list and give it an article schema
	@test -f '$(SITE_LIST)' || { echo "no site list at $(SITE_LIST); set SITE_LIST="; exit 1; }
	$(SCOUR) add $(SITE_ITEM) --template article
	$(SCOUR) add $(SITE_ITEM) --type html
	$(SCOUR) import $(SITE_ITEM) --urls '$(SITE_LIST)'

.PHONY: news-articles-crawl
news-articles-crawl: build ## Crawl the news sites, bounded by pages and time
	$(SCOUR) crawl $(SITE_ITEM) --depth $(SITE_DEPTH) \
		--max-pages $(SITE_PAGES) --max-time $(SITE_TIME)

.PHONY: news-articles-train
news-articles-train: build ## Learn where an article's fields live on a page
	$(SCOUR) train $(SITE_ITEM)

.PHONY: news-articles-export
news-articles-export: build ## Write the articles out, one file per domain
	$(SCOUR) export $(SITE_ITEM) --format $(EXPORT_FORMAT)

# Microdata is the third shape, and the easiest one. A page carrying schema.org
# attributes or OpenGraph tags has already declared what it is, so extraction
# is reading a label rather than inferring one. It is worth running over the
# same sites as a separate item: what a page says about itself and what its
# markup implies are different claims, and comparing them is how you find out
# which to trust.

MICRO_ITEM  ?= news-microdata
MICRO_PAGES   ?= 1000
MICRO_TIME    ?= 20m
MICRO_DEPTH   ?= 2

.PHONY: news-microdata
news-microdata: news-microdata-import news-microdata-crawl news-microdata-train news-microdata-export ## Import, crawl, train and export declared structured data

.PHONY: news-microdata-import
news-microdata-import: build ## Load the site list with a schema.org and OpenGraph schema
	@test -f '$(SITE_LIST)' || { echo "no site list at $(SITE_LIST); set SITE_LIST="; exit 1; }
	$(SCOUR) add $(MICRO_ITEM) --template microdata
	$(SCOUR) add $(MICRO_ITEM) --type html
	$(SCOUR) import $(MICRO_ITEM) --urls '$(SITE_LIST)'

.PHONY: news-microdata-crawl
news-microdata-crawl: build ## Crawl for pages that declare their own structure
	$(SCOUR) crawl $(MICRO_ITEM) --depth $(MICRO_DEPTH) \
		--max-pages $(MICRO_PAGES) --max-time $(MICRO_TIME)

.PHONY: news-microdata-train
news-microdata-train: build ## Learn where the declared fields live
	$(SCOUR) train $(MICRO_ITEM)

.PHONY: news-microdata-export
news-microdata-export: build ## Write the declared data out, one file per domain
	$(SCOUR) export $(MICRO_ITEM) --format $(EXPORT_FORMAT)

.PHONY: news-status
news-status: build ## Show both corpora side by side
	$(SCOUR) status

.PHONY: news-clean
news-clean: build ## Delete the news items and everything crawled for them
	-$(SCOUR) remove $(FEED_ITEM)
	-$(SCOUR) remove $(SITE_ITEM)
	-$(SCOUR) remove $(MICRO_ITEM)

.PHONY: news
news: news-feeds news-articles news-microdata ## Run all three news pipelines end to end
