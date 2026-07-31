// SPDX-License-Identifier: MIT

package parse

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parseJS builds a binding tree under doc from a JavaScript body. Only the
// parts that can hold extractable data are kept — function and class scopes,
// declared names, object literal properties, and literal values — which is
// enough to reach the inline state blobs pages ship as `window.__DATA__ = {...}`
// while ignoring control flow entirely.
func parseJS(doc *graph.Node, body []byte) error {
	ast, err := js.Parse(parse.NewInputBytes(body), js.Options{})
	if err != nil {
		return fmt.Errorf("parse js: %w", err)
	}
	v := &jsVisitor{stack: []*graph.Node{doc}}
	js.Walk(v, ast)
	return nil
}

// jsVisitor turns the AST walk into tree construction. It pushes a node for
// every construct that can contain values and pops it on exit, so the node
// stack always mirrors the walk depth.
type jsVisitor struct {
	stack []*graph.Node
}

func (v *jsVisitor) Enter(n js.INode) js.IVisitor {
	if name, kind, push := jsNode(n); push {
		parent := v.stack[len(v.stack)-1]
		v.stack = append(v.stack, parent.Append(graph.New(kind, name, "")))
		return v
	}
	if lit, ok := n.(*js.LiteralExpr); ok {
		if text := jsLiteral(lit); text != "" {
			v.stack[len(v.stack)-1].Append(graph.New(graph.KindLiteral, "", text))
		}
	}
	return v
}

func (v *jsVisitor) Exit(n js.INode) {
	if _, _, push := jsNode(n); push && len(v.stack) > 1 {
		v.stack = v.stack[:len(v.stack)-1]
	}
}

// jsNode reports whether an AST node opens a named container, and with which
// kind. Enter and Exit both consult it so pushes and pops stay balanced.
func jsNode(n js.INode) (name string, kind graph.Kind, push bool) {
	switch t := n.(type) {
	case *js.FuncDecl:
		if t.Name != nil {
			return string(t.Name.Data), graph.KindScope, true
		}
		return "", graph.KindScope, true
	case *js.ClassDecl:
		if t.Name != nil {
			return string(t.Name.Data), graph.KindScope, true
		}
		return "", graph.KindScope, true
	case *js.BindingElement:
		if v, ok := t.Binding.(*js.Var); ok {
			return string(v.Data), graph.KindBinding, true
		}
		return "", graph.KindBinding, false
	case *js.Property:
		if t.Name != nil && t.Name.IsSet() {
			return unquote(string(t.Name.Literal.Data)), graph.KindBinding, true
		}
		return "", graph.KindBinding, false
	}
	return "", graph.KindRoot, false
}

// jsLiteral renders a literal as matchable text, dropping the ones that carry
// no data (null, undefined, regexes).
func jsLiteral(lit *js.LiteralExpr) string {
	s := string(lit.Data)
	switch lit.TokenType {
	case js.StringToken, js.TemplateToken:
		return unquote(s)
	case js.NumericToken, js.DecimalToken, js.IntegerToken, js.BinaryToken,
		js.OctalToken, js.HexadecimalToken:
		return s
	case js.TrueToken, js.FalseToken:
		return s
	}
	return ""
}

// unquote strips the surrounding quotes or backticks from a JavaScript string
// literal and resolves the escapes that matter for text matching.
func unquote(s string) string {
	if len(s) >= 2 {
		switch s[0] {
		case '\'', '"', '`':
			if s[len(s)-1] == s[0] {
				s = s[1 : len(s)-1]
			}
		}
	}
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	// Reuse the Go unquoter for the common escapes, falling back to the raw
	// text when the literal uses syntax Go does not accept.
	if out, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return out
	}
	return s
}
