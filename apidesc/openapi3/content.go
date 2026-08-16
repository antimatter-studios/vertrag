package openapi3

import (
	"fmt"
	"regexp"
	"strings"
)

// statusCodePattern matches an exact HTTP status code. A range such as `2XX`
// deliberately does not match: the reference gives ranges no status code, so
// they fall through to the compiler's default of 200.
var statusCodePattern = regexp.MustCompile(`^\d{3}$`)

// parseRequestBody turns a Request Body Object into one message per media type.
//
// A document that offers the same operation as JSON and as form data describes
// two distinct exchanges, and each is tested separately.
func (d *document) parseRequestBody(n node) []message {
	body := d.Resolve(n)
	if !body.IsMapping() {
		return nil
	}
	return d.parseContent(body.Get("content"), false)
}

// parseResponses turns a Responses Object into the responses to expect.
func (d *document) parseResponses(n node) []message {
	if !n.IsMapping() {
		return nil
	}

	var responses []message
	for _, member := range n.Entries() {
		key := member.Key.Str()

		// Only status codes, ranges and `default` describe responses; anything
		// else in this position is not a response at all.
		if !statusCodePattern.MatchString(key) && key != "default" && !isStatusCodeRange(key) {
			continue
		}

		response := d.Resolve(member.Value)
		if !response.IsMapping() {
			continue
		}

		messages := d.parseContent(response.Get("content"), true)
		if len(messages) == 0 {
			messages = []message{{}}
		}

		headers := d.parseResponseHeaders(response.Get("headers"))

		for _, msg := range messages {
			// A range or `default` carries no status code of its own. Leaving
			// it unset is what makes the compiler fall back to 200, which is
			// the behaviour being reproduced.
			if statusCodePattern.MatchString(key) {
				msg.statusCode = key
			}
			msg.headers = headers
			responses = append(responses, msg)
		}
	}
	return responses
}

func isStatusCodeRange(key string) bool {
	if len(key) != 3 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < '0' || c > '9') && c != 'X' {
			return false
		}
	}
	return true
}

// parseResponseHeaders reads a Headers Object.
//
// The values are left empty because the document declares what a header will
// be, not what it will contain; the compiler carries the name through so the
// response can be checked for its presence.
func (d *document) parseResponseHeaders(n node) []header {
	var headers []header
	for _, member := range n.Entries() {
		declared := d.Resolve(member.Value)
		value := ""
		if example, ok := schemaExample(d.Resolve(declared.Get("schema"))); ok {
			value = stringifyScalar(example)
		}
		if example := declared.Get("example"); example.Valid() {
			value = stringifyScalar(scalarValue(example))
		}
		headers = append(headers, header{name: member.Key.Str(), value: value})
	}
	return headers
}

// parseContent turns a Content Object into the messages it describes.
//
// One per media type, and one per named example where a media type carries
// several: a document giving an Examples Object with "accepted" and "rejected"
// entries is describing two exchanges, not one illustrated twice.
//
// withSchema controls whether a JSON Schema is attached. It is emitted for
// responses only: it exists so a consumer can validate what came back, and
// there is nothing to validate on the way out.
func (d *document) parseContent(content node, withSchema bool) []message {
	var messages []message

	for _, member := range content.Entries() {
		mediaType := member.Key.Str()
		mediaTypeObject := member.Value
		schema := mediaTypeObject.Get("schema")

		for _, example := range d.examplesOf(mediaTypeObject, mediaType) {
			msg := message{
				contentType: mediaType,
				example:     example.name,
				body:        example.body,
				hasBody:     example.hasBody,
			}

			// A multipart body is assembled from the schema's properties rather
			// than serialised, and the boundary that separates the parts has to
			// reach the Content-Type header too, or the server cannot read it.
			//
			// Dredd sends nothing here, which is why projects testing file
			// uploads end up skipping those endpoints entirely.
			if !msg.hasBody && isMultipartMediaType(mediaType) && schema.Valid() {
				if body, ok := d.multipartBody(schema, multipartBoundary); ok {
					msg.body, msg.hasBody = body, true
					msg.contentType = mediaType + "; boundary=" + multipartBoundary
				}
			}

			if !msg.hasBody && schema.Valid() {
				if value, ok := d.generateValue(schema, nil); ok {
					if body, ok := renderBody(value, mediaType); ok {
						msg.body, msg.hasBody = body, true
					}
				}
			}

			if withSchema && schema.Valid() && isJSONMediaType(mediaType) {
				if converted, ok := d.convertSchema(schema); ok {
					msg.schema = converted
				}
			}

			messages = append(messages, msg)
		}
	}

	return messages
}

// namedBody is one body a media type describes, under the name the document
// gave it.
type namedBody struct {
	name    string
	body    string
	hasBody bool
}

// examplesOf reads the bodies a Media Type Object describes.
//
// Always at least one, so a media type with no example at all still yields a
// message to be filled in from its schema.
func (d *document) examplesOf(mediaTypeObject node, mediaType string) []namedBody {
	// A single `example` is what the author chose to demonstrate and takes
	// precedence over anything else, including an Examples Object beside it.
	if example := mediaTypeObject.Get("example"); example.Valid() {
		if body, ok := renderBody(scalarValue(example), mediaType); ok {
			return []namedBody{{body: body, hasBody: true}}
		}
		return []namedBody{{}}
	}

	var out []namedBody
	for _, member := range mediaTypeObject.Get("examples").Entries() {
		name := member.Key.Str()
		example := d.Resolve(member.Value)

		value := example.Get("value")
		if !value.Valid() {
			// An externalValue points at a body vertrag has not fetched, so
			// there is nothing to send. The name is still carried, so the
			// exchange exists and its body comes from the schema.
			out = append(out, namedBody{name: name})
			continue
		}
		if body, ok := renderBody(scalarValue(value), mediaType); ok {
			out = append(out, namedBody{name: name, body: body, hasBody: true})
			continue
		}
		out = append(out, namedBody{name: name})
	}

	if len(out) == 0 {
		return []namedBody{{}}
	}
	return out
}

// multipartBoundary separates the parts of a generated multipart body.
//
// It is fixed rather than random so that two runs of the same description
// produce byte-identical requests. A contract test that differs run to run
// cannot be diffed, and a random boundary would change every recorded body.
const multipartBoundary = "vertrag-boundary"

// multipartBody assembles a multipart payload from a schema's properties.
//
// Each property becomes one part. A property declaring a binary format gets
// placeholder bytes and a filename, since that is what a server parsing an
// upload expects to find; anything else is sent as its generated value.
func (d *document) multipartBody(schema node, boundary string) (string, bool) {
	resolved := d.Resolve(schema)
	properties := resolved.Get("properties").Entries()
	if len(properties) == 0 {
		return "", false
	}

	var body strings.Builder
	for _, property := range properties {
		name := property.Key.Str()
		field := d.Resolve(property.Value)

		disposition := fmt.Sprintf("form-data; name=%q", name)
		content := ""

		if isBinarySchema(field) {
			disposition += fmt.Sprintf("; filename=%q", name)
			// Enough bytes to be a file without pretending to be a real one.
			content = "vertrag placeholder"
		} else if value, ok := d.generateValue(field, nil); ok {
			content = stringifyScalar(value)
		}

		fmt.Fprintf(&body, "--%s\r\nContent-Disposition: %s\r\n\r\n%s\r\n", boundary, disposition, content)
	}
	fmt.Fprintf(&body, "--%s--\r\n", boundary)

	return body.String(), true
}

// isBinarySchema reports whether a property describes file content.
func isBinarySchema(schema node) bool {
	if schema.Get("type").Str() != "string" {
		return false
	}
	switch schema.Get("format").Str() {
	case "binary", "base64", "byte":
		return true
	}
	return false
}

func isMultipartMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(baseMediaType(mediaType)), "multipart/")
}
