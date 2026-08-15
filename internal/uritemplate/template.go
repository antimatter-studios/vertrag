// Package uritemplate expands URI templates the way Dredd does.
//
// This is a port of the npm `uri-template` package, not of RFC 6570. The
// distinction matters: that library deviates from the RFC in several places,
// and Dredd's observable behaviour — the URIs it requests and the warnings it
// emits — is the library's behaviour, not the specification's. Reproducing the
// spec instead would make the two implementations disagree on real API
// descriptions. Each deviation is marked below with why it is kept.
package uritemplate

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Param is a single variable reference inside an expression, e.g. `id`, `id*`
// or `id:4`.
type Param struct {
	Name    string
	Explode bool
	Cut     int
	HasCut  bool
}

// Expression is one `{...}` group, together with the literal text that follows
// it in the template.
type Expression struct {
	Op     byte // 0 for the simple operator
	Params []Param
	Suffix string

	first string
	sep   string
	named bool
	empty string
	// reserved selects the "U+R" encoder, which passes reserved characters
	// through unescaped.
	reserved bool
}

// Template is a parsed URI template.
type Template struct {
	Prefix      string
	Expressions []*Expression
}

// Parse reads a URI template.
//
// The grammar mirrors the reference's PEG: a template is a sequence of literal
// runs and `{op params}` expressions, where a param name is drawn from
// [a-zA-Z0-9_.%] and may carry a `*` explode marker or a `:N` prefix length.
//
// Failures are reported the way the generated parser reports them — naming
// every token that could have appeared at the furthest position reached, not
// just the one the last rule wanted. Dredd surfaces this message verbatim as a
// parser annotation, so the wording is part of its output, not an internal
// detail.
func Parse(template string) (*Template, error) {
	p := &parser{input: template, rightmostPos: 0}
	t := &Template{}

	for p.pos < len(p.input) {
		if p.input[p.pos] != '{' {
			start := p.pos
			for p.pos < len(p.input) && p.input[p.pos] != '{' {
				p.pos++
			}
			literal := p.input[start:p.pos]
			if len(t.Expressions) == 0 {
				t.Prefix += literal
			} else {
				t.Expressions[len(t.Expressions)-1].Suffix += literal
			}
			continue
		}
		// A literal run cannot start here, which the reference records as a
		// failed `[^{]` match before it tries to read an expression.
		p.fail("[^{]")

		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if expr == nil {
			break
		}
		t.Expressions = append(t.Expressions, expr)
	}

	if p.pos != len(p.input) {
		return nil, p.syntaxError()
	}
	return t, nil
}

// parser tracks the position reached and the tokens that would have allowed it
// to go further, which together produce the reference's error message.
type parser struct {
	input        string
	pos          int
	rightmostPos int
	expected     []string
}

// fail records that a token could have matched at the current position.
//
// Only the furthest position is kept: an alternative that failed earlier is not
// interesting once the parser has moved past it.
func (p *parser) fail(token string) {
	if p.pos < p.rightmostPos {
		return
	}
	if p.pos > p.rightmostPos {
		p.rightmostPos = p.pos
		p.expected = nil
	}
	p.expected = append(p.expected, token)
}

// parseExpression reads one `{...}` group. It returns nil, nil when the input
// does not form an expression, leaving the position where it started so the
// caller can report the failure.
func (p *parser) parseExpression() (*Expression, error) {
	start := p.pos

	if p.pos >= len(p.input) || p.input[p.pos] != '{' {
		p.fail(`"{"`)
		p.pos = start
		return nil, nil
	}
	p.pos++

	// The operator is optional, so a miss is recorded but does not fail.
	var op byte
	if p.pos < len(p.input) && strings.IndexByte("/;:.?&+#", p.input[p.pos]) >= 0 {
		op = p.input[p.pos]
		p.pos++
	} else {
		p.fail(`[\/;:.?&+#]`)
	}

	params := p.parseParamList()

	if p.pos >= len(p.input) || p.input[p.pos] != '}' {
		p.fail(`"}"`)
		p.pos = start
		return nil, nil
	}
	p.pos++

	expr, err := newExpression(op, params)
	if err != nil {
		return nil, err
	}
	return expr, nil
}

func (p *parser) parseParamList() []Param {
	params := []Param{p.parseParam()}
	for p.pos < len(p.input) && p.input[p.pos] == ',' {
		p.pos++
		params = append(params, p.parseParam())
	}
	p.fail(`","`)
	return params
}

// parseParam reads one variable reference. The name may be empty: the
// reference's rule matches zero or more name characters, so `{}` parses as a
// single unnamed parameter rather than failing here.
func (p *parser) parseParam() Param {
	start := p.pos
	for p.pos < len(p.input) && isParamNameByte(p.input[p.pos]) {
		p.pos++
	}
	p.fail(`[a-zA-Z0-9_.%]`)
	param := Param{Name: p.input[start:p.pos]}

	// The reference tries the `:N` prefix before the `*` explode marker, and
	// records both when neither matches.
	if p.pos < len(p.input) && p.input[p.pos] == ':' {
		p.pos++
		digitStart := p.pos
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			p.pos++
		}
		p.fail(`[0-9]`)
		if digitStart != p.pos {
			if n, err := strconv.Atoi(p.input[digitStart:p.pos]); err == nil {
				param.Cut = n
				param.HasCut = true
			}
		} else {
			p.pos = digitStart - 1
		}
	} else {
		p.fail(`":"`)
		if p.pos < len(p.input) && p.input[p.pos] == '*' {
			param.Explode = true
			p.pos++
		} else {
			p.fail(`"*"`)
		}
	}

	// An `(...)` extension is accepted and discarded, as in the reference.
	if p.pos < len(p.input) && p.input[p.pos] == '(' {
		if end := strings.IndexByte(p.input[p.pos:], ')'); end > 1 {
			p.pos += end + 1
		}
	} else {
		p.fail(`"("`)
	}

	return param
}

// syntaxError renders the failure the way the generated parser does: the
// expected tokens sorted and de-duplicated, and the character actually found.
func (p *parser) syntaxError() error {
	offset := p.pos
	if p.rightmostPos > offset {
		offset = p.rightmostPos
	}

	expected := append([]string(nil), p.expected...)
	sort.Strings(expected)
	expected = slices.Compact(expected)

	var expectedText string
	switch len(expected) {
	case 0:
		expectedText = "end of input"
	case 1:
		expectedText = expected[0]
	default:
		expectedText = strings.Join(expected[:len(expected)-1], ", ") + " or " + expected[len(expected)-1]
	}

	found := "end of input"
	if offset < len(p.input) {
		found = quoteJS(string(p.input[offset]))
	}

	return fmt.Errorf("Expected %s but %s found.", expectedText, found)
}

// quoteJS renders a string the way the generated parser's own quote() helper
// does, so the message matches character for character.
func quoteJS(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r > 0x7e {
				b.WriteString(escapeJS(r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func escapeJS(r rune) string {
	if r <= 0xff {
		return fmt.Sprintf(`\x%02X`, r)
	}
	return fmt.Sprintf(`\u%04X`, r)
}

func isParamNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '.', c == '%':
		return true
	}
	return false
}

// newExpression selects the expansion rules for an operator.
func newExpression(op byte, params []Param) (*Expression, error) {
	e := &Expression{Op: op, Params: params, sep: ",", empty: ""}
	switch op {
	case 0: // simple
	case '+':
		e.reserved = true
	case '#':
		e.first, e.empty, e.reserved = "#", "#", true
	case '.':
		e.first, e.sep, e.empty = ".", ".", "."
	case '/':
		e.first, e.sep = "/", "/"
	case ';':
		e.first, e.sep, e.named = ";", ";", true
	case '?':
		e.first, e.sep, e.named, e.empty = "?", "&", true, "="
	case '&':
		e.first, e.sep, e.named, e.empty = "&", "&", true, "="
	case ':':
		// The reference's grammar accepts ':' as an operator but defines no
		// expansion class for it, so constructing the expression throws. The
		// error is reproduced rather than the character being treated as a
		// simple operator, because callers surface it as a parse failure and
		// skip the transaction entirely.
		return nil, fmt.Errorf("unsupported URI template operator %q", string(op))
	default:
		return nil, fmt.Errorf("unsupported URI template operator %q", string(op))
	}
	return e, nil
}

// Expand renders the template with the given variables.
func (t *Template) Expand(vars map[string]any) string {
	var b strings.Builder
	b.WriteString(t.Prefix)
	for _, expr := range t.Expressions {
		b.WriteString(expr.expand(vars))
	}
	return b.String()
}

func (e *Expression) expand(vars map[string]any) string {
	type pair struct {
		param Param
		value any
	}

	var defined []pair
	for _, p := range e.Params {
		v, present := vars[p.Name]
		if !present || !isDefined(v) {
			continue
		}
		defined = append(defined, pair{p, v})
	}

	parts := make([]string, 0, len(defined))
	for _, d := range defined {
		parts = append(parts, e.expandPair(d.param, d.value))
	}
	expanded := strings.Join(parts, e.sep)

	if expanded != "" {
		return e.first + expanded + e.Suffix
	}
	if e.empty != "" && len(defined) > 0 {
		return e.empty + e.Suffix
	}
	return e.Suffix
}

// isDefined mirrors the reference's `definedPairs` filter.
//
// Scalars are always defined, empty string included — which is why a `{?q}`
// with q="" still renders as "?q=". Collections are defined only when they hold
// at least one truthy member, so [] , [0] and {} are all treated as absent.
func isDefined(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case []any:
		for _, item := range value {
			if truthy(item) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, item := range value {
			if truthy(item) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func truthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0
	case int:
		return value != 0
	default:
		return true
	}
}

func (e *Expression) expandPair(param Param, value any) string {
	if param.Explode {
		switch v := value.(type) {
		case []any:
			return e.explodeArray(param, v)
		case string:
			return e.stringifySingle(param, v)
		case map[string]any:
			return e.explodeObject(v)
		default:
			return e.stringifySingle(param, value)
		}
	}
	return e.stringifySingle(param, value)
}

func (e *Expression) explodeArray(param Param, array []any) string {
	parts := make([]string, 0, len(array))
	for _, item := range array {
		if e.named {
			parts = append(parts, param.Name+"="+e.encode(scalarString(item)))
		} else {
			parts = append(parts, e.encode(scalarString(item)))
		}
	}
	return strings.Join(parts, e.sep)
}

func (e *Expression) explodeObject(object map[string]any) string {
	// The reference iterates the object in JavaScript property order. Go maps
	// have no order, so keys are sorted to at least be deterministic. Dredd
	// only ever expands scalar URI parameters, so this path is unreachable
	// from its own compiler; the oracle would flag it if that ever changed.
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		encodedKey := e.encode(k)
		if items, ok := object[k].([]any); ok {
			for _, item := range items {
				pairs = append(pairs, encodedKey+"="+e.encode(scalarString(item)))
			}
			continue
		}
		pairs = append(pairs, encodedKey+"="+e.encode(scalarString(object[k])))
	}
	return strings.Join(pairs, e.sep)
}

// stringifySingle encodes one value, applying the `:N` prefix if present and
// the name= prefix for the named operators.
func (e *Expression) stringifySingle(param Param, value any) string {
	var encoded string
	switch v := value.(type) {
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, e.encode(scalarString(item)))
		}
		encoded = strings.Join(parts, ",")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, e.encode(k)+","+e.encode(scalarString(v[k])))
		}
		encoded = strings.Join(parts, ",")
	default:
		s := scalarString(value)
		// The prefix counts UTF-16 code units, as JavaScript's substring does.
		if param.HasCut {
			s = truncateUTF16(s, param.Cut)
		}
		encoded = e.encode(s)
	}

	if !e.named {
		return encoded
	}
	if encoded != "" {
		return param.Name + "=" + encoded
	}
	return param.Name + e.empty
}

func scalarString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		// JavaScript renders integral floats without a decimal point.
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
