package uritemplate

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

// encode percent-encodes a value for inclusion in a URI, using whichever of the
// reference's two character sets this expression's operator selects.
func (e *Expression) encode(s string) string {
	if e.reserved {
		return encodeReserved(s)
	}
	return encodeUnreserved(s)
}

// encodeUnreserved is the reference's "U" encoder: /[^\w~.-]/g.
//
// Everything outside [A-Za-z0-9_~.-] is escaped.
func encodeUnreserved(s string) string {
	return encodeUnits(s, func(units []uint16, i int) bool {
		return !isUnreservedUnit(units[i])
	})
}

// encodeReserved is the reference's "U+R" encoder:
// /[^\w.~:\/\?#\[\]@!\$&'()*+,;=%-]|%(?!\d\d)/g
//
// Reserved characters pass through, and a percent sign is left alone only when
// two DIGITS follow it. That test is `\d\d`, not a hex-digit test, so an
// already-escaped sequence such as %2F is escaped a second time into %252F.
// It is a defect in the reference, but it is observable in the URIs Dredd
// requests, so it is reproduced here deliberately.
func encodeReserved(s string) string {
	return encodeUnits(s, func(units []uint16, i int) bool {
		c := units[i]
		if c == '%' {
			return !(i+2 < len(units) && isDigitUnit(units[i+1]) && isDigitUnit(units[i+2]))
		}
		return !isReservedUnit(c)
	})
}

// encodeUnits walks the string as UTF-16 code units, matching JavaScript's
// string model. Characters outside the Basic Multilingual Plane are therefore
// encoded as their surrogate halves.
func encodeUnits(s string, shouldEncode func(units []uint16, i int) bool) string {
	units := utf16.Encode([]rune(s))
	var b strings.Builder
	for i := 0; i < len(units); i++ {
		if !shouldEncode(units, i) {
			b.WriteRune(rune(units[i]))
			continue
		}
		writePercentEncoded(&b, units[i])
	}
	return b.String()
}

// writePercentEncoded emits one code unit as UTF-8-ish percent escapes.
//
// The hex is NOT zero-padded, because the reference builds each byte with
// `c.toString(16).toUpperCase()` and no padding. A code unit that encodes to a
// byte below 0x10 therefore yields a one-digit escape such as %A rather than
// %0A. Padding it would be more correct and would disagree with Dredd.
func writePercentEncoded(b *strings.Builder, c uint16) {
	var bytes []int
	switch {
	case c < 128:
		bytes = []int{int(c)}
	case c < 2048:
		bytes = []int{int(c>>6) | 192, int(c&63) | 128}
	default:
		bytes = []int{int(c>>12) | 224, int((c>>6)&63) | 128, int(c&63) | 128}
	}
	for _, v := range bytes {
		b.WriteByte('%')
		b.WriteString(strings.ToUpper(strconv.FormatInt(int64(v), 16)))
	}
}

func isUnreservedUnit(c uint16) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '~', c == '.', c == '-':
		return true
	}
	return false
}

func isReservedUnit(c uint16) bool {
	if isUnreservedUnit(c) {
		return true
	}
	return strings.IndexByte(":/?#[]@!$&'()*+,;=%", byte(c)) >= 0 && c < 128
}

func isDigitUnit(c uint16) bool { return c >= '0' && c <= '9' }

// truncateUTF16 keeps the first n UTF-16 code units, as JavaScript's
// String.prototype.substring does.
func truncateUTF16(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if n >= len(units) {
		return s
	}
	return string(utf16.Decode(units[:n]))
}
