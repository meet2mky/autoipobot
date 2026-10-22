package flow

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Qualifier expressions:
//
//	pageContains("css selector")
//	waitFor("css selector", seconds)     true if the selector appears within the timeout
//	true | false
//	NOT expr | expr AND expr | expr OR expr | ( expr )
//
// Keywords and function names are case-insensitive; AND binds tighter than OR.

// Expr is a parsed qualifier expression.
type Expr struct {
	op       string // literal, contains, waitfor, not, and, or
	value    bool
	selector string
	timeout  time.Duration
	left     *Expr
	right    *Expr
}

// Prober answers page questions for expression evaluation.
type Prober interface {
	PageContains(ctx context.Context, selector string) (bool, error)
}

var exprToken = regexp.MustCompile(`(?i)pageContains\s*\(\s*"(?:\\.|[^"\\])*"\s*\)|waitFor\s*\(\s*"(?:\\.|[^"\\])*"\s*,\s*[0-9]+(?:\.[0-9]+)?\s*\)|\bAND\b|\bOR\b|\bNOT\b|\bTRUE\b|\bFALSE\b|[()]`)

type exprParser struct {
	tokens []string
	at     int
}

// ParseExpression parses source into an Expr.
func ParseExpression(source string) (*Expr, error) {
	var tokens []string
	cursor := 0
	for _, span := range exprToken.FindAllStringIndex(source, -1) {
		if gap := strings.TrimSpace(source[cursor:span[0]]); gap != "" {
			return nil, fmt.Errorf("unexpected text %q", gap)
		}
		tokens = append(tokens, strings.TrimSpace(source[span[0]:span[1]]))
		cursor = span[1]
	}
	if rest := strings.TrimSpace(source[cursor:]); rest != "" {
		return nil, fmt.Errorf("unexpected text %q", rest)
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expression is empty")
	}
	parser := &exprParser{tokens: tokens}
	node, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if parser.at != len(tokens) {
		return nil, fmt.Errorf("unexpected token %q", tokens[parser.at])
	}
	return node, nil
}

func (p *exprParser) peekIs(word string) bool {
	return p.at < len(p.tokens) && strings.EqualFold(p.tokens[p.at], word)
}

func (p *exprParser) parseOr() (*Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peekIs("OR") {
		p.at++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &Expr{op: "or", left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (*Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.peekIs("AND") {
		p.at++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &Expr{op: "and", left: left, right: right}
	}
	return left, nil
}

func (p *exprParser) parseUnary() (*Expr, error) {
	if p.peekIs("NOT") {
		p.at++
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &Expr{op: "not", left: child}, nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (*Expr, error) {
	if p.at >= len(p.tokens) {
		return nil, fmt.Errorf("expected a condition")
	}
	token := p.tokens[p.at]
	p.at++
	switch {
	case token == "(":
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.at >= len(p.tokens) || p.tokens[p.at] != ")" {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		p.at++
		return node, nil
	case token == ")":
		return nil, fmt.Errorf("unexpected closing parenthesis")
	case strings.EqualFold(token, "TRUE"):
		return &Expr{op: "literal", value: true}, nil
	case strings.EqualFold(token, "FALSE"):
		return &Expr{op: "literal"}, nil
	case strings.HasPrefix(strings.ToLower(token), "pagecontains"):
		selector, err := quotedArgument(token)
		if err != nil {
			return nil, err
		}
		return &Expr{op: "contains", selector: selector}, nil
	case strings.HasPrefix(strings.ToLower(token), "waitfor"):
		selector, err := quotedArgument(token)
		if err != nil {
			return nil, err
		}
		comma := strings.LastIndex(token, ",")
		close := strings.LastIndex(token, ")")
		seconds, err := strconv.ParseFloat(strings.TrimSpace(token[comma+1:close]), 64)
		if err != nil || seconds <= 0 || seconds > 3600 {
			return nil, fmt.Errorf("waitFor timeout must be between 0 and 3600 seconds")
		}
		return &Expr{op: "waitfor", selector: selector, timeout: time.Duration(seconds * float64(time.Second))}, nil
	default:
		return nil, fmt.Errorf("expected pageContains(\"selector\"), waitFor(\"selector\", seconds), true, false, or a parenthesized expression")
	}
}

func quotedArgument(token string) (string, error) {
	start := strings.Index(token, `"`)
	end := strings.LastIndex(token, `"`)
	if start < 0 || end <= start {
		return "", fmt.Errorf("missing quoted selector in %q", token)
	}
	selector, err := strconv.Unquote(token[start : end+1])
	if err != nil {
		return "", fmt.Errorf("invalid selector string in %q: %w", token, err)
	}
	if strings.TrimSpace(selector) == "" {
		return "", fmt.Errorf("selector in %q is empty", token)
	}
	return selector, nil
}

// Selectors lists every selector referenced by the expression.
func (e *Expr) Selectors() []string {
	if e == nil {
		return nil
	}
	var out []string
	if e.selector != "" {
		out = append(out, e.selector)
	}
	out = append(out, e.left.Selectors()...)
	return append(out, e.right.Selectors()...)
}

// WaitPoll is the interval used by waitFor; variable for tests.
var WaitPoll = 500 * time.Millisecond

// Eval evaluates the expression. values are applied to selectors so that
// expressions may contain @placeholders.
func (e *Expr) Eval(ctx context.Context, prober Prober, values map[string]string) (bool, error) {
	if e == nil {
		return false, fmt.Errorf("empty expression")
	}
	switch e.op {
	case "literal":
		return e.value, nil
	case "contains":
		return prober.PageContains(ctx, Resolve(e.selector, values))
	case "waitfor":
		selector := Resolve(e.selector, values)
		deadline := time.Now().Add(e.timeout)
		for {
			found, err := prober.PageContains(ctx, selector)
			if err != nil {
				return false, err
			}
			if found {
				return true, nil
			}
			if time.Now().After(deadline) {
				return false, nil
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(WaitPoll):
			}
		}
	case "not":
		value, err := e.left.Eval(ctx, prober, values)
		return !value, err
	case "and":
		left, err := e.left.Eval(ctx, prober, values)
		if err != nil || !left {
			return left, err
		}
		return e.right.Eval(ctx, prober, values)
	case "or":
		left, err := e.left.Eval(ctx, prober, values)
		if err != nil || left {
			return left, err
		}
		return e.right.Eval(ctx, prober, values)
	default:
		return false, fmt.Errorf("unknown expression operation %q", e.op)
	}
}
