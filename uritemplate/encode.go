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
// string model, because whether a character is reserved is decided unit by unit
// and the reference does the same.
//
// A surrogate PAIR is rejoined before being encoded. Dredd escapes each half
// separately, which produces CESU-8 — `🎉` becomes %ED%A0%BC%ED%BE%89 rather
// than %F0%9F%8E%89 — and a server decoding that gets two lone surrogates,
// which is not valid UTF-8 and not the character anybody sent. An API taking
// emoji in a search term or a name receives mojibake.
func encodeUnits(s string, shouldEncode func(units []uint16, i int) bool) string {
	units := utf16.Encode([]rune(s))
	var b strings.Builder

	for i := 0; i < len(units); i++ {
		if !shouldEncode(units, i) {
			b.WriteRune(rune(units[i]))
			continue
		}

		// A high surrogate followed by a low one is a single character split
		// across two units. Encoding them apart is what produces CESU-8.
		if isHighSurrogate(units[i]) && i+1 < len(units) && isLowSurrogate(units[i+1]) {
			writeUTF8(&b, utf16.DecodeRune(rune(units[i]), rune(units[i+1])))
			i++
			continue
		}
		writePercentEncoded(&b, units[i])
	}
	return b.String()
}

func isHighSurrogate(c uint16) bool { return c >= 0xD800 && c <= 0xDBFF }
func isLowSurrogate(c uint16) bool  { return c >= 0xDC00 && c <= 0xDFFF }

// writeUTF8 percent-encodes a rune as the bytes UTF-8 actually uses.
func writeUTF8(b *strings.Builder, r rune) {
	for _, octet := range []byte(string(r)) {
		writeOctet(b, octet)
	}
}

// writePercentEncoded emits one code unit as percent escapes.
//
// Dredd builds each byte with `c.toString(16).toUpperCase()` and no padding, so
// a byte below 0x10 yields a one-digit escape: a newline becomes %A rather than
// %0A. That is not merely untidy, it is malformed, and it corrupts the
// surrounding text — `a\nb` becomes `a%Ab`, which a server parses as the single
// byte 0xAB, swallowing the `b` entirely. The value the server receives is
// neither what was sent nor the same length.
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
		writeOctet(b, byte(v))
	}
}

// writeOctet emits one byte as a percent escape, always two hex digits.
func writeOctet(b *strings.Builder, octet byte) {
	b.WriteByte('%')
	hex := strings.ToUpper(strconv.FormatInt(int64(octet), 16))
	if len(hex) == 1 {
		b.WriteByte('0')
	}
	b.WriteString(hex)
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
