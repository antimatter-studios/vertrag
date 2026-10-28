package validate

import (
	"strings"
	"testing"
)

// byteranges builds a multipart/byteranges body with the given boundary, one
// part per range, as RFC 9110 §14.6 writes it.
func byteranges(boundary string, parts ...[2]string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: application/octet-stream\r\n")
		b.WriteString("Content-Range: " + part[0] + "\r\n\r\n")
		b.WriteString(part[1] + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

// TestAMultipartBodyIsComparedPartByPart is #121: a multipart answer's
// boundary is chosen per response, so the body is compared part by part, on
// each part's headers and content, and never on the boundary.
func TestAMultipartBodyIsComparedPartByPart(t *testing.T) {
	expected := Message{
		StatusCode: "206",
		Headers:    map[string]string{"Content-Type": "multipart/byteranges"},
		Body:       byteranges("EXAMPLE", [2]string{"bytes 0-1/10", "ab"}, [2]string{"bytes 4-5/10", "ef"}),
	}
	answered := func(body string) Message {
		return Message{StatusCode: "206",
			Headers: map[string]string{"Content-Type": "multipart/byteranges; boundary=3d6b6a416f9b5"}, Body: body}
	}

	same := answered(byteranges("3d6b6a416f9b5", [2]string{"bytes 0-1/10", "ab"}, [2]string{"bytes 4-5/10", "ef"}))
	if result := Validate(expected, same); !result.Valid {
		t.Errorf("the same parts under another boundary failed: %v", result.Fields["body"].Errors)
	}

	for name, body := range map[string]string{
		"different content": byteranges("3d6b6a416f9b5", [2]string{"bytes 0-1/10", "ab"}, [2]string{"bytes 4-5/10", "xx"}),
		"different range":   byteranges("3d6b6a416f9b5", [2]string{"bytes 0-1/10", "ab"}, [2]string{"bytes 6-7/10", "ef"}),
		"a part missing":    byteranges("3d6b6a416f9b5", [2]string{"bytes 0-1/10", "ab"}),
		"not multipart":     "abef",
	} {
		if result := Validate(expected, answered(body)); result.Valid {
			t.Errorf("%s passed", name)
		}
	}
}
