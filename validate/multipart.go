package validate

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// multipartBoundary is the boundary a multipart message declares: the
// boundary parameter of its Content-Type, or, for a documented example whose
// media type carries none, the delimiter its body opens with.
func multipartBoundary(headers map[string]string, body string) (string, bool) {
	for name, value := range headers {
		if !strings.EqualFold(name, "content-type") {
			continue
		}
		media, params, err := mime.ParseMediaType(value)
		if err != nil || !strings.HasPrefix(media, "multipart/") {
			return "", false
		}
		if boundary := params["boundary"]; boundary != "" {
			return boundary, true
		}
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if boundary, ok := strings.CutPrefix(line, "--"); ok && boundary != "" {
			return boundary, true
		}
		return "", false
	}
	return "", false
}

// isMultipartContent reports whether the expected message documents a
// multipart media type.
func isMultipartContent(headers map[string]string) bool {
	for name, value := range headers {
		if strings.EqualFold(name, "content-type") {
			media, _, err := mime.ParseMediaType(value)
			return err == nil && strings.HasPrefix(media, "multipart/")
		}
	}
	return false
}

// multipartPart is what a part is compared on: the headers that say what it
// carries, and its content.
type multipartPart struct {
	contentType, contentRange, content string
}

func readMultipart(body, boundary string) ([]multipartPart, error) {
	reader := multipart.NewReader(strings.NewReader(body), boundary)
	var parts []multipartPart
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		parts = append(parts, multipartPart{
			contentType:  part.Header.Get("Content-Type"),
			contentRange: part.Header.Get("Content-Range"),
			content:      string(content),
		})
	}
}

// validateMultipart compares a multipart body part by part (#121). A
// multipart answer's boundary is chosen per response, and a 206 to a
// multi-range request (RFC 9110 §14.6) never repeats its example's, so the
// boundary, preamble and epilogue are not compared: each part's Content-Type,
// Content-Range and content are.
func validateMultipart(expected, real Message) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("text"), Errors: []string{}}
	fail := func(format string, args ...any) FieldResult {
		field.Valid = false
		field.Errors = append(field.Errors, fmt.Sprintf(format, args...))
		return field
	}

	expectedBoundary, ok := multipartBoundary(nil, expected.Body)
	if !ok {
		return validateText(expected.Body, real.Body)
	}
	want, err := readMultipart(expected.Body, expectedBoundary)
	if err != nil {
		return validateText(expected.Body, real.Body)
	}
	realBoundary, ok := multipartBoundary(real.Headers, real.Body)
	if !ok {
		return fail("Expected a multipart body, but the response declares no boundary.")
	}
	got, err := readMultipart(real.Body, realBoundary)
	if err != nil {
		return fail("The multipart body could not be read: %v.", err)
	}
	if len(got) != len(want) {
		return fail("Expected %d multipart part(s), but got %d.", len(want), len(got))
	}
	for i := range want {
		switch {
		case want[i].contentType != got[i].contentType:
			fail("Part %d: expected Content-Type '%s', but got '%s'.", i+1, want[i].contentType, got[i].contentType)
		case want[i].contentRange != got[i].contentRange:
			fail("Part %d: expected Content-Range '%s', but got '%s'.", i+1, want[i].contentRange, got[i].contentRange)
		case want[i].content != got[i].content:
			fail("Part %d: actual and expected data do not match.", i+1)
		}
	}
	return field
}
