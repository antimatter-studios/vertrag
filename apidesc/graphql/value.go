package graphql

import (
	"strconv"
)

// EnumValue is an enum member appearing as a value.
//
// It is a distinct type rather than a string for a reason that bites as soon as
// anything is generated from a schema: an enum value is written UNQUOTED in a
// GraphQL document, and a string is written quoted. A default of `status: ACTIVE`
// arriving as the Go string "ACTIVE" would be rendered back as `"ACTIVE"`, which
// is a different value the server will reject. Keeping the distinction in the
// type is the only way a consumer cannot get it wrong by accident.
type EnumValue string

func (e EnumValue) String() string { return string(e) }

// parseLiteral reads a value written as GraphQL source text.
//
// It exists so that both input forms produce the same Go values. SDL writes a
// default inline, as tokens; introspection reports it as a STRING containing
// the same source text — `defaultValue: "[1, 2]"`, not a JSON array — which is
// a detail that would otherwise leave every default from an introspected schema
// as an unusable string.
func parseLiteral(literal string) (any, error) {
	tokens, err := tokenise([]byte(literal))
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}

	value, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if !p.at(tokenEOF) {
		return nil, p.unexpected("the end of the value")
	}
	return value, nil
}

// parseValue reads one value literal.
//
// The Go types it produces are the contract with everything downstream:
//
//	Int      int64
//	Float    float64
//	String   string
//	Boolean  bool
//	Null     nil
//	Enum     EnumValue
//	List     []any
//	Object   map[string]any
//
// Int is int64 rather than float64 because GraphQL's Int and Float are
// different types and a server will reject `1.0` where an Int belongs — the
// distinction the lexer keeps has to survive to here or it was pointless.
func (p *parser) parseValue() (any, error) {
	tok := p.current()

	switch tok.kind {
	case tokenInt:
		p.advance()
		if value, err := strconv.ParseInt(tok.text, 10, 64); err == nil {
			return value, nil
		}
		// Beyond int64. The specification's Int is only 32 bits, so this is
		// already a schema doing something unusual; it is kept as a number
		// rather than rejected, because the document did write a number.
		value, err := strconv.ParseFloat(tok.text, 64)
		if err != nil {
			return nil, positionError(tok.line, tok.col, "%s is not a number this can represent", tok.text)
		}
		return value, nil

	case tokenFloat:
		p.advance()
		value, err := strconv.ParseFloat(tok.text, 64)
		if err != nil {
			return nil, positionError(tok.line, tok.col, "%s is not a number this can represent", tok.text)
		}
		return value, nil

	case tokenString:
		p.advance()
		return tok.text, nil

	case tokenName:
		p.advance()
		switch tok.text {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			// A written null and no default at all are different things, which
			// is what the HasDefault flag beside every Default exists to say.
			return nil, nil
		}
		return EnumValue(tok.text), nil

	case tokenPunct:
		switch tok.text {
		case "[":
			return p.parseListValue()
		case "{":
			return p.parseObjectValue()
		case "$":
			p.advance()
			name := ""
			if p.at(tokenName) {
				name = p.current().text
			}
			return nil, positionError(tok.line, tok.col,
				"a default value cannot use the variable $%s: defaults are constants", name)
		}
	}

	return nil, p.unexpected("a value")
}

func (p *parser) parseListValue() (any, error) {
	p.advance() // [

	// Empty rather than nil, so that a written `[]` is distinguishable from a
	// default that was never given once it has been through an `any`.
	values := []any{}
	for !p.atPunct("]") {
		if p.at(tokenEOF) {
			return nil, p.unexpected("`]`")
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	p.advance()
	return values, nil
}

// parseObjectValue reads an input-object literal.
//
// The result is a map, which loses the order the fields were written in. That
// is deliberate: an input object is unordered by definition, and a consumer
// rendering one back into a query has to pick a stable order of its own anyway
// — sorting the keys is what makes generated requests reproducible.
func (p *parser) parseObjectValue() (any, error) {
	p.advance() // {

	values := map[string]any{}
	for !p.atPunct("}") {
		if p.at(tokenEOF) {
			return nil, p.unexpected("`}`")
		}
		name, err := p.expectName("an input field name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expectPunct(":"); err != nil {
			return nil, err
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if _, duplicate := values[name.text]; duplicate {
			return nil, positionError(name.line, name.col,
				"this object value sets the field %s twice", name.text)
		}
		values[name.text] = value
	}
	p.advance()
	return values, nil
}
