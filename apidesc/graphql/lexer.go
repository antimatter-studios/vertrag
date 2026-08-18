package graphql

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The SDL is lexed by hand because vertrag ships a static binary and treats
// every dependency as a liability. The GraphQL parsers for Go are each larger
// than this whole package and each pull in far more than a lexer; the language
// they read has a dozen keywords and eight token kinds, which is a day's work
// to lex properly and no work at all to maintain afterwards.
//
// Lexing is a separate stage from parsing for one reason that earns its keep:
// every token carries the line and column it started at, so a schema that will
// not parse is reported at the place it went wrong rather than as "invalid
// GraphQL". Schemas are usually thousands of lines long and often generated,
// and "invalid" is not an actionable thing to tell their author.

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenPunct
	tokenName
	tokenInt
	tokenFloat
	tokenString
)

// token is one lexical unit of a schema document.
//
// text holds the source spelling for names, punctuation and numbers, and the
// DECODED value for strings — escapes resolved and block-string indentation
// removed. Descriptions are the main consumer and they want the text a reader
// sees, not the bytes that spell it.
type token struct {
	kind tokenKind
	text string
	line int
	col  int

	// afterComma records that a comma was skipped immediately before this
	// token. Commas are ignored tokens in GraphQL, so nothing should depend on
	// one — with a single exception, the pre-2018 `implements A, B` spelling,
	// where the comma is the only thing that distinguishes a second interface
	// name from the start of the next definition. See parseImplements.
	afterComma bool
}

// describe names a token the way an error message needs to refer to it.
func (t token) describe() string {
	switch t.kind {
	case tokenEOF:
		return "the end of the document"
	case tokenString:
		return "a string"
	default:
		return "`" + t.text + "`"
	}
}

type lexer struct {
	src string
	pos int
	// line and lineStart are carried rather than recomputed: a block string can
	// span a hundred lines, and counting newlines from the top of the document
	// for every token after it is the kind of quadratic behaviour that only
	// shows up on the large generated schemas this is meant for.
	line      int
	lineStart int
	// sawComma is whether the run of ignored tokens before the token now being
	// scanned contained a comma. See token.afterComma.
	sawComma bool
}

// tokenise reads a whole document up front.
//
// Streaming would save memory on a large schema and cost the parser its
// lookahead: SDL needs two tokens of it in places — `extend` then the keyword
// it extends, a description then whatever it describes — and an indexable
// slice makes that free. A schema is a few megabytes at the very worst.
func tokenise(source []byte) ([]token, error) {
	// A byte-order mark is an ignored token in GraphQL, and only at the start.
	l := &lexer{src: strings.TrimPrefix(string(source), "\ufeff"), line: 1}

	var out []token
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
		if tok.kind == tokenEOF {
			return out, nil
		}
	}
}

func (l *lexer) next() (token, error) {
	l.sawComma = false
	l.skipIgnored()

	tok, err := l.scan()
	if err != nil {
		return token{}, err
	}
	tok.afterComma = l.sawComma
	return tok, nil
}

// scan reads the token starting at the current position, which the ignored
// tokens before it have already been skipped past.
func (l *lexer) scan() (token, error) {
	line, col := l.line, l.pos-l.lineStart+1
	if l.pos >= len(l.src) {
		return token{kind: tokenEOF, line: line, col: col}, nil
	}

	c := l.src[l.pos]
	switch {
	case strings.HasPrefix(l.src[l.pos:], `"""`):
		text, err := l.scanBlockString(line, col)
		if err != nil {
			return token{}, err
		}
		return token{kind: tokenString, text: text, line: line, col: col}, nil

	case c == '"':
		text, err := l.scanString(line, col)
		if err != nil {
			return token{}, err
		}
		return token{kind: tokenString, text: text, line: line, col: col}, nil

	case isNameStart(c):
		start := l.pos
		for l.pos < len(l.src) && isNameContinue(l.src[l.pos]) {
			l.pos++
		}
		return token{kind: tokenName, text: l.src[start:l.pos], line: line, col: col}, nil

	case c == '-' || isDigit(c):
		text, kind, err := l.scanNumber(line, col)
		if err != nil {
			return token{}, err
		}
		return token{kind: kind, text: text, line: line, col: col}, nil

	case strings.HasPrefix(l.src[l.pos:], "..."):
		l.pos += 3
		return token{kind: tokenPunct, text: "...", line: line, col: col}, nil

	case strings.IndexByte("!$&()[]{}|:=@", c) >= 0:
		l.pos++
		return token{kind: tokenPunct, text: string(c), line: line, col: col}, nil
	}

	r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
	return token{}, positionError(line, col, "%q is not a character GraphQL uses", r)
}

// skipIgnored consumes what GraphQL calls ignored tokens.
//
// Commas are among them, which is why `implements A, B` — the spelling the
// pre-2018 SDL used — and `implements A & B` both work here without the parser
// knowing there are two spellings: the comma never reaches it.
func (l *lexer) skipIgnored() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t':
			l.pos++
		case ',':
			l.sawComma = true
			l.pos++
		case '\n':
			l.newline(1)
		case '\r':
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '\n' {
				l.newline(2)
				continue
			}
			l.newline(1)
		case '#':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

func (l *lexer) newline(width int) {
	l.pos += width
	l.line++
	l.lineStart = l.pos
}

// skipTo advances over a region already scanned, keeping the line count right.
func (l *lexer) skipTo(end int) {
	for l.pos < end {
		switch l.src[l.pos] {
		case '\n':
			l.newline(1)
		case '\r':
			if l.pos+1 < end && l.src[l.pos+1] == '\n' {
				l.newline(2)
				continue
			}
			l.newline(1)
		default:
			l.pos++
		}
	}
}

func (l *lexer) scanString(line, col int) (string, error) {
	l.pos++ // the opening quote

	var b strings.Builder
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == '"':
			l.pos++
			return b.String(), nil

		case c == '\n' || c == '\r':
			return "", positionError(line, col,
				"this string is not closed before the end of its line; a description spanning lines is written with \"\"\"")

		case c == '\\':
			l.pos++
			if l.pos >= len(l.src) {
				return "", positionError(line, col, "this string is not closed")
			}
			switch e := l.src[l.pos]; e {
			case '"', '\\', '/':
				b.WriteByte(e)
				l.pos++
			case 'b':
				b.WriteByte('\b')
				l.pos++
			case 'f':
				b.WriteByte('\f')
				l.pos++
			case 'n':
				b.WriteByte('\n')
				l.pos++
			case 'r':
				b.WriteByte('\r')
				l.pos++
			case 't':
				b.WriteByte('\t')
				l.pos++
			case 'u':
				l.pos++
				r, err := l.scanUnicodeEscape()
				if err != nil {
					return "", err
				}
				b.WriteRune(r)
			default:
				return "", positionError(l.line, l.pos-l.lineStart+1,
					"\\%c is not a string escape GraphQL defines", e)
			}

		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	return "", positionError(line, col, "this string is not closed")
}

// scanUnicodeEscape reads the escape after its `\u`.
//
// Both spellings are accepted: the fixed four-digit form, whose surrogate pairs
// have to be recombined by hand, and the braced form the 2021 specification
// added for code points above the basic plane.
func (l *lexer) scanUnicodeEscape() (rune, error) {
	line, col := l.line, l.pos-l.lineStart+1

	if l.pos < len(l.src) && l.src[l.pos] == '{' {
		end := strings.IndexByte(l.src[l.pos:], '}')
		if end < 0 {
			return 0, positionError(line, col, "this \\u{...} escape is not closed")
		}
		digits := l.src[l.pos+1 : l.pos+end]
		value, err := strconv.ParseUint(digits, 16, 32)
		if err != nil || rune(value) > unicode.MaxRune {
			return 0, positionError(line, col, "\\u{%s} is not a Unicode code point", digits)
		}
		l.pos += end + 1
		return rune(value), nil
	}

	if l.pos+4 > len(l.src) {
		return 0, positionError(line, col, "a \\u escape needs four hexadecimal digits")
	}
	digits := l.src[l.pos : l.pos+4]
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return 0, positionError(line, col, "\\u%s is not four hexadecimal digits", digits)
	}
	l.pos += 4

	first := rune(value)
	if utf16.IsSurrogate(first) && strings.HasPrefix(l.src[l.pos:], `\u`) && l.pos+6 <= len(l.src) {
		if second, err := strconv.ParseUint(l.src[l.pos+2:l.pos+6], 16, 32); err == nil {
			if combined := utf16.DecodeRune(first, rune(second)); combined != utf8.RuneError {
				l.pos += 6
				return combined, nil
			}
		}
	}
	return first, nil
}

func (l *lexer) scanBlockString(line, col int) (string, error) {
	var raw strings.Builder
	for i := l.pos + 3; i < len(l.src); {
		// The only escape a block string has, so that a literal `"""` can
		// appear inside one.
		if strings.HasPrefix(l.src[i:], `\"""`) {
			raw.WriteString(`"""`)
			i += 4
			continue
		}
		if strings.HasPrefix(l.src[i:], `"""`) {
			l.skipTo(i + 3)
			return dedentBlockString(raw.String()), nil
		}
		raw.WriteByte(l.src[i])
		i++
	}
	return "", positionError(line, col, "this block string is not closed")
}

// dedentBlockString applies the specification's BlockStringValue.
//
// Without it every description in a schema arrives carrying the indentation of
// the type it was written inside, which then appears in whatever a consumer
// prints. The rule is that the first line keeps its indentation — it starts
// immediately after the opening quotes — and every other line loses the
// smallest indentation any non-blank one has.
func dedentBlockString(raw string) string {
	lines := splitLines(raw)

	common := -1
	for _, line := range lines[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if width := len(line) - len(trimmed); common < 0 || width < common {
			common = width
		}
	}
	if common > 0 {
		for i := 1; i < len(lines); i++ {
			if len(lines[i]) >= common {
				lines[i] = lines[i][common:]
				continue
			}
			lines[i] = strings.TrimLeft(lines[i], " \t")
		}
	}

	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// scanNumber reads an Int or a Float, telling them apart the way the
// specification does: a fraction or an exponent makes a Float and nothing else
// does. `1` and `1.0` are different literals for a schema's purposes, because
// the first satisfies an Int argument and the second does not.
func (l *lexer) scanNumber(line, col int) (string, tokenKind, error) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
	}
	if l.digits() == 0 {
		return "", tokenEOF, positionError(line, col, "a number needs at least one digit")
	}

	kind := tokenInt
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		kind = tokenFloat
		l.pos++
		if l.digits() == 0 {
			return "", tokenEOF, positionError(line, col, "a number needs a digit after its decimal point")
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		kind = tokenFloat
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if l.digits() == 0 {
			return "", tokenEOF, positionError(line, col, "a number needs a digit after its exponent")
		}
	}
	return l.src[start:l.pos], kind, nil
}

func (l *lexer) digits() int {
	count := 0
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
		count++
	}
	return count
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameContinue(c byte) bool {
	return isNameStart(c) || isDigit(c)
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// positionError is how every failure to read a document is phrased: the place
// first, then what was wrong there.
func positionError(line, col int, format string, args ...any) error {
	return fmt.Errorf("line %d, column %d: %s", line, col, fmt.Sprintf(format, args...))
}
