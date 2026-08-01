// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rangertaha/scour/internal/defaults"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/version"
)

// MCP builds the Model Context Protocol server.
//
// The tools mirror the command line rather than the database, because an agent
// driving scour is doing the same job a person does: describe an item, crawl
// for it, train on what came back, read the results, correct them. Exposing
// tables instead would make the agent reimplement the workflow.
//
// Crawling and training return a job id rather than blocking. An agent that
// waits minutes inside one tool call has no way to report progress and every
// chance of timing out.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "scour",
		Version: version.Version(),
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_items",
		Description: "List the items scour knows about and how many records " +
			"have been extracted for each.",
	}, s.mcpListItems)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_item",
		Description: "Describe one item: its aliases, properties, crawl targets " +
			"and content types.",
	}, s.mcpGetItem)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "add_item",
		Description: "Create an item or add to an existing one. Every field is " +
			"optional and additive, so calling this twice with the same arguments " +
			"changes nothing the second time. Give aliases and property examples: " +
			"they are what scour scores links against before it has crawled anything.",
	}, s.mcpAddItem)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_templates",
		Description: "List the built-in schemas that can be passed as the template " +
			"argument to add_item. A template fills in a kind of record's usual " +
			"properties, the words a page might label them with, and an example of each.",
	}, s.mcpTemplates)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "crawl",
		Description: "Start crawling an item's targets. Returns a job id " +
			"immediately; poll job_status to see how it went.",
	}, s.mcpCrawl)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "train",
		Description: "Learn extraction rules and a link scoring model from the pages " +
			"already crawled. Returns a job id; poll job_status.",
	}, s.mcpTrain)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "job_status",
		Description: "Check a crawl or training job started earlier.",
	}, s.mcpJob)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_rules",
		Description: "List the extraction rules learned for an item, with how often each fires.",
	}, s.mcpRules)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_records",
		Description: "Search the records extracted for an item.",
	}, s.mcpSearch)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "label_record",
		Description: "Mark an extracted record as correct or wrong. Labels are what " +
			"the next training run learns from, so correcting a few records is the " +
			"most direct way to improve extraction.",
	}, s.mcpLabel)

	return srv
}

// MCPHandler serves MCP over HTTP, for an agent that attaches to a running
// service rather than spawning one.
func (s *Server) MCPHandler() http.Handler {
	srv := s.MCP()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

// text is the human-readable half of a tool result. The SDK also returns the
// structured output, but a model reads the text, and a bare JSON blob with no
// sentence around it is harder to act on than one line of prose.
func text(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

type itemName struct {
	Name string `json:"name" jsonschema:"the item's name"`
}

type listItemsOut struct {
	Items []store.ItemSummary `json:"items"`
}

func (s *Server) mcpListItems(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listItemsOut, error) {
	rows, err := s.store.Items(ctx)
	if err != nil {
		return nil, listItemsOut{}, err
	}

	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, fmt.Sprintf("%s (%s)", row.Name, plural(row.Matches, "record")))
	}
	if len(names) == 0 {
		return text("No items yet. Use add_item to define one."), listItemsOut{}, nil
	}
	return text("%s", strings.Join(names, ", ")), listItemsOut{Items: rows}, nil
}

type getItemOut struct {
	Item *store.Item `json:"item"`
}

func (s *Server) mcpGetItem(ctx context.Context, _ *mcp.CallToolRequest, in itemName) (*mcp.CallToolResult, getItemOut, error) {
	item, err := s.store.ItemFull(ctx, in.Name)
	if err != nil {
		return nil, getItemOut{}, err
	}

	props := make([]string, 0, len(item.Properties))
	for _, p := range item.Properties {
		props = append(props, p.Name)
	}
	return text("%s: %d targets, %d aliases, properties: %s",
			item.Name, len(item.AllTargets()), len(item.Aliases),
			strings.Join(props, ", ")),
		getItemOut{Item: item}, nil
}

type addItemIn struct {
	Name     string   `json:"name" jsonschema:"the item's name"`
	Aliases  []string `json:"aliases,omitempty" jsonschema:"other words a page might use for this item"`
	Domains  []string `json:"domains,omitempty" jsonschema:"whole domains to crawl"`
	URLs     []string `json:"urls,omitempty" jsonschema:"single pages to start from"`
	Template string   `json:"template,omitempty" jsonschema:"a built-in schema to start from; see list_templates"`
	Props    []struct {
		Name    string `json:"name" jsonschema:"the property's name"`
		Type    string `json:"type,omitempty" jsonschema:"string, number or date"`
		Example string `json:"example,omitempty" jsonschema:"a real value from the site being crawled"`
	} `json:"properties,omitempty" jsonschema:"the fields each record should have"`
}

type addItemOut struct {
	Name    string   `json:"name"`
	Changes []string `json:"changes"`
}

func (s *Server) mcpAddItem(ctx context.Context, _ *mcp.CallToolRequest, in addItemIn) (*mcp.CallToolResult, addItemOut, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, addItemOut{}, fmt.Errorf("name is required")
	}

	item, err := s.store.CreateItem(ctx, in.Name)
	if err != nil {
		return nil, addItemOut{}, err
	}

	var changes []string
	if in.Template != "" {
		if err := applyTemplate(ctx, s.store, item.ID, in.Template); err != nil {
			return nil, addItemOut{}, err
		}
		changes = append(changes, "template "+in.Template)
	}
	for _, alias := range in.Aliases {
		if err := s.store.AddAlias(ctx, item.ID, alias); err != nil {
			return nil, addItemOut{}, err
		}
		changes = append(changes, "alias "+alias)
	}
	job, err := s.store.JobForItem(ctx, item)
	if err != nil {
		return nil, addItemOut{}, err
	}

	for _, d := range in.Domains {
		if err := s.store.AddTarget(ctx, job.ID, store.TargetDomain, d, false, 0); err != nil {
			return nil, addItemOut{}, err
		}
		changes = append(changes, "domain "+d)
	}
	for _, u := range in.URLs {
		if err := s.store.AddTarget(ctx, job.ID, store.TargetURL, u, false, 0); err != nil {
			return nil, addItemOut{}, err
		}
		changes = append(changes, "url "+u)
	}
	for _, p := range in.Props {
		if err := s.store.AddProperty(ctx, item.ID, p.Name, p.Type, p.Example); err != nil {
			return nil, addItemOut{}, err
		}
		changes = append(changes, "property "+p.Name)
	}

	out := addItemOut{Name: item.Name, Changes: changes}
	if len(changes) == 0 {
		return text("Item %s exists; nothing to add.", item.Name), out, nil
	}
	return text("%s: %s", item.Name, strings.Join(changes, ", ")), out, nil
}

type templatesOut struct {
	Templates []templateInfo `json:"templates"`
}

type templateInfo struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

func (s *Server) mcpTemplates(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, templatesOut, error) {
	names, err := defaults.Names()
	if err != nil {
		return nil, templatesOut{}, err
	}

	var out templatesOut
	var lines []string
	for _, name := range names {
		schema, err := defaults.Schema(name)
		if err != nil {
			return nil, templatesOut{}, err
		}

		props := schema
		if len(schema) == 1 && len(schema[0].Props) > 0 {
			props = schema[0].Props
		}

		fields := make([]string, 0, len(props))
		for _, p := range props {
			fields = append(fields, p.Name)
		}
		out.Templates = append(out.Templates, templateInfo{Name: name, Fields: fields})
		lines = append(lines, fmt.Sprintf("%s: %s", name, strings.Join(fields, ", ")))
	}
	return text("%s", strings.Join(lines, "\n")), out, nil
}

type crawlIn struct {
	Name     string `json:"name" jsonschema:"the item to crawl"`
	Depth    int    `json:"depth,omitempty" jsonschema:"how many links deep to follow"`
	MaxPages int    `json:"max_pages,omitempty" jsonschema:"stop after this many pages"`
	Browser  string `json:"browser,omitempty" jsonschema:"never, auto or always"`
}

type jobOut struct {
	Job Job `json:"job"`
}

func (s *Server) mcpCrawl(ctx context.Context, _ *mcp.CallToolRequest, in crawlIn) (*mcp.CallToolResult, jobOut, error) {
	job, err := s.crawlJob(ctx, in.Name, crawlRequest{
		Depth: in.Depth, MaxPages: in.MaxPages, Browser: in.Browser,
	})
	if err != nil {
		return nil, jobOut{}, err
	}
	return text("Crawling %s as job %s. Poll job_status with that id.", in.Name, job.ID),
		jobOut{Job: *job}, nil
}

func (s *Server) mcpTrain(ctx context.Context, _ *mcp.CallToolRequest, in itemName) (*mcp.CallToolResult, jobOut, error) {
	job, err := s.trainJob(ctx, in.Name, trainRequest{})
	if err != nil {
		return nil, jobOut{}, err
	}
	return text("Training %s as job %s. Poll job_status with that id.", in.Name, job.ID),
		jobOut{Job: *job}, nil
}

type jobID struct {
	ID string `json:"id" jsonschema:"the job id returned by crawl or train"`
}

func (s *Server) mcpJob(_ context.Context, _ *mcp.CallToolRequest, in jobID) (*mcp.CallToolResult, jobOut, error) {
	job, ok := s.jobs.Get(in.ID)
	if !ok {
		return nil, jobOut{}, fmt.Errorf("no job %q", in.ID)
	}

	switch job.State {
	case Running:
		return text("Job %s is still running, %s so far.", job.ID, job.Elapsed().Round(1e9)), jobOut{Job: job}, nil
	case Failed:
		return text("Job %s failed: %s", job.ID, job.Error), jobOut{Job: job}, nil
	default:
		return text("Job %s finished in %s.", job.ID, job.Elapsed().Round(1e9)), jobOut{Job: job}, nil
	}
}

type rulesOut struct {
	Rules []store.Rule `json:"rules"`
}

func (s *Server) mcpRules(ctx context.Context, _ *mcp.CallToolRequest, in itemName) (*mcp.CallToolResult, rulesOut, error) {
	item, err := s.store.Item(ctx, in.Name)
	if err != nil {
		return nil, rulesOut{}, err
	}

	rows, err := s.store.Rules(ctx, item.ID)
	if err != nil {
		return nil, rulesOut{}, err
	}
	if len(rows) == 0 {
		return text("No rules yet for %s. Crawl some pages, then train.", in.Name), rulesOut{}, nil
	}
	return text("%d rules for %s.", len(rows), in.Name), rulesOut{Rules: rows}, nil
}

type searchIn struct {
	Name       string  `json:"name" jsonschema:"the item to search"`
	Confidence float64 `json:"confidence,omitempty" jsonschema:"only records at or above this confidence"`
	Limit      int     `json:"limit,omitempty" jsonschema:"cap the number of records returned"`
	Label      string  `json:"label,omitempty" jsonschema:"valid, invalid or unlabelled"`
}

type searchOut struct {
	Records []store.RecordRow `json:"records"`
	Total   int64             `json:"total"`
}

func (s *Server) mcpSearch(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	item, err := s.store.Item(ctx, in.Name)
	if err != nil {
		return nil, searchOut{}, err
	}

	// Unbounded results would be a way to fill an agent's context with a single
	// call, so a search with no limit still gets one.
	limit := in.Limit
	if limit <= 0 {
		limit = defaultRecordLimit
	}

	rows, total, err := s.store.SearchRecords(ctx, item.ID, store.RecordQuery{
		MinConfidence: in.Confidence,
		Label:         store.Label(in.Label),
		Limit:         limit,
	})
	if err != nil {
		return nil, searchOut{}, err
	}
	return text("%d of %d records for %s.", len(rows), total, in.Name),
		searchOut{Records: rows, Total: total}, nil
}

// defaultRecordLimit bounds a search that asked for no limit.
const defaultRecordLimit = 50

// plural renders a count with its noun, so a tool result reads as a sentence
// rather than as a template someone forgot to finish.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

type labelIn struct {
	Name  string `json:"name" jsonschema:"the item the record belongs to"`
	ID    uint   `json:"id" jsonschema:"the record id from search_records"`
	Label string `json:"label" jsonschema:"valid, invalid or unlabelled"`
}

type labelOut struct {
	ID    uint   `json:"id"`
	Label string `json:"label"`
}

func (s *Server) mcpLabel(ctx context.Context, _ *mcp.CallToolRequest, in labelIn) (*mcp.CallToolResult, labelOut, error) {
	item, err := s.store.Item(ctx, in.Name)
	if err != nil {
		return nil, labelOut{}, err
	}

	label := store.Label(strings.ToLower(strings.TrimSpace(in.Label)))
	switch label {
	case store.Valid, store.Invalid, store.Unlabelled:
	default:
		return nil, labelOut{}, fmt.Errorf("label must be valid, invalid or unlabelled, got %q", in.Label)
	}

	n, err := s.store.LabelRecords(ctx, item.ID, []uint{in.ID}, label)
	if err != nil {
		return nil, labelOut{}, err
	}
	if n == 0 {
		return nil, labelOut{}, fmt.Errorf("no record %d for %s", in.ID, in.Name)
	}
	return text("Record %d marked %s. Retrain to learn from it.", in.ID, label),
		labelOut{ID: in.ID, Label: string(label)}, nil
}
