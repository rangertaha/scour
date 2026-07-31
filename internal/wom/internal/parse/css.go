// SPDX-License-Identifier: MIT

package parse

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parseCSS builds a rule tree under doc from a stylesheet. Rulesets become
// graph.KindRule named by their selector text, at-rules with a block become
// graph.KindAtRule, and declarations become graph.KindDecl carrying the property name and
// value. Stylesheets rarely hold the data a schema asks for, but they do carry
// content values (::before/content, background-image URLs) and they let the
// graph answer "which rule styles this selector".
func parseCSS(doc *graph.Node, body []byte) error {
	p := css.NewParser(parse.NewInputBytes(body), false)
	stack := []*graph.Node{doc}
	var produced int

	for {
		gt, _, data := p.Next()
		if gt == css.ErrorGrammar {
			// The tokenizer reports end-of-input and syntax errors the same
			// way. Real stylesheets are full of small mistakes, so a partial
			// parse is kept — but a body that yielded nothing at all was
			// never CSS, and accepting it as an empty document would hide the
			// mistake behind a graph with nothing in it.
			err := p.Err()
			if err != nil && !errors.Is(err, io.EOF) && produced == 0 {
				return fmt.Errorf("parse css: %w", err)
			}
			return nil
		}

		switch gt {
		case css.BeginRulesetGrammar:
			sel := tokenText(p.Values())
			stack = append(stack, stack[len(stack)-1].Append(graph.New(graph.KindRule, sel, "")))
			produced++
		case css.BeginAtRuleGrammar:
			name := strings.TrimSpace(string(data) + " " + tokenText(p.Values()))
			stack = append(stack, stack[len(stack)-1].Append(graph.New(graph.KindAtRule, name, "")))
			produced++
		case css.EndRulesetGrammar, css.EndAtRuleGrammar:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case css.AtRuleGrammar:
			// An at-rule with no block, such as @import or @charset.
			stack[len(stack)-1].Append(graph.New(graph.KindAtRule, string(data), tokenText(p.Values())))
			produced++
		case css.DeclarationGrammar, css.CustomPropertyGrammar:
			stack[len(stack)-1].Append(graph.New(graph.KindDecl, string(data), tokenText(p.Values())))
			produced++
		case css.CommentGrammar, css.TokenGrammar:
			// Not addressable content.
		}
	}
}

// tokenText joins a run of CSS tokens back into source text, inserting spaces
// only where the tokens were separated by whitespace to begin with.
func tokenText(tokens []css.Token) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.TokenType == css.WhitespaceToken {
			b.WriteByte(' ')
			continue
		}
		b.Write(t.Data)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
