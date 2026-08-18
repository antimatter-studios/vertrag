package openapi3

import (
	"fmt"
	"regexp"
	"strings"
)

// statusCodePattern matches an exact HTTP status code, and statusRangePattern
// one of the five bands OpenAPI defines as Response Object keys.
//
// Only `1XX` to `5XX` are ranges. `22X` and `XXX` are neither a code nor a
// band, and were accepted as ranges until now — which gave a typo a meaning
// its author never wrote, in the one place where getting it wrong means
// expecting the wrong response.
var (
	statusCodePattern  = regexp.MustCompile(`^\d{3}$`)
	statusRangePattern = regexp.MustCompile(`^[1-5]XX$`)
)

// parseRequestBody turns a Request Body Object into one message per media type.
//
// A document that offers the same operation as JSON and as form data describes
// two distinct exchanges, and each is tested separately.
func (d *document) parseRequestBody(n node) []message {
	body := d.Resolve(n)
	if !body.IsMapping() {
		return nil
	}
	return d.parseContentFor(body.Get("content"), true, inRequest)
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

		messages := d.parseContentFor(response.Get("content"), true, inResponse)
		if len(messages) == 0 {
			messages = []message{{}}
		}

		headers := d.parseResponseHeaders(response.Get("headers"))
		links := d.parseLinks(response.Get("links"))

		for _, msg := range messages {
			// A range travels as itself, `2XX` and all. It used to be dropped
			// here, which left the compiler to fall back to 200 — so a
			// document saying "some success" was tested as though it had said
			// "exactly 200", and a server answering the 201 its own document
			// permits was reported as wrong. The band is carried through to
			// the runner, which knows how to judge one.
			//
			// `default` is still dropped, and deliberately. It is not a band:
			// it means "every code not described here", a set that depends on
			// the operation's other keys and that no status expectation can
			// state. Leaving it to the compiler's 200 is what vertrag has
			// always done and what the reference does, and changing it is a
			// separate decision from this one.
			if statusCodePattern.MatchString(key) || isStatusCodeRange(key) {
				msg.statusCode = key
			}
			msg.headers = headers
			msg.links = links
			responses = append(responses, msg)
		}
	}
	return responses
}

func isStatusCodeRange(key string) bool {
	return statusRangePattern.MatchString(key)
}

// parseResponseHeaders reads a Headers Object.
//
// A value is only a demonstration — the document declares what a header will be,
// not what it will contain — so the name is what matters downstream: the
// compiler carries it through and the response is checked for its presence.
//
// `required` is deliberately not carried. Gavel demands every header the
// description declares, whether or not it was marked required, so an absent one
// already fails through ordinary validation; recording the flag would let it
// look as though something acted on it.
func (d *document) parseResponseHeaders(n node) []header {
	var headers []header
	for _, member := range n.Entries() {
		name := member.Key.Str()
		declared := d.Resolve(member.Value)
		value := ""
		if example, ok := schemaExample(d.Resolve(declared.Get("schema"))); ok {
			value = stringifyScalar(example)
		}
		if example := declared.Get("example"); example.Valid() {
			value = stringifyScalar(scalarValue(example))
		}
		headers = append(headers, header{
			name:   name,
			value:  value,
			schema: d.headerSchema(name, declared),
		})
	}
	return headers
}

// headerSchema converts a Header Object's schema into the JSON Schema its value
// is checked against, or "" for a declaration vertrag will not act on.
//
// A Header Object borrows the Parameter Object's serialisation rules, and only
// the default `simple` style is read back off the wire. A document choosing
// another style, or describing its header with `content` rather than a schema,
// is saying the text is not the plain rendering of the schema — and checking it
// as though it were would fail servers doing exactly what they were told.
//
// A `Content-Type` entry is dropped because the specification says a Header
// Object of that name is to be ignored. The media type belongs to the Response
// Object's content map, which vertrag already compares.
func (d *document) headerSchema(name string, declared node) string {
	if strings.EqualFold(name, "content-type") {
		return ""
	}
	if style := declared.Get("style").Str(); style != "" && style != "simple" {
		return ""
	}
	converted, ok := d.convertSchema(declared.Get("schema"))
	if !ok {
		return ""
	}
	return converted
}

// parseContent turns a Content Object into the messages it describes.
//
// One per media type, and one per named example where a media type carries
// several: a document giving an Examples Object with "accepted" and "rejected"
// entries is describing two exchanges, not one illustrated twice.
//
// withSchema controls whether a JSON Schema is attached. On a response it is
// what the body is validated against; on a request it is what generation draws
// from, since testing an operation with inputs the description permits needs
// the shape of a valid body rather than the single instance the example shows.
func (d *document) parseContent(content node, withSchema bool) []message {
	return d.parseContentFor(content, withSchema, inResponse)
}

// parseContentFor is parseContent told which half of the exchange it is
// reading, so `readOnly` and `writeOnly` can mean what the specification says
// they mean rather than being reported as unsupported.
func (d *document) parseContentFor(content node, withSchema bool, dir direction) []message {
	var messages []message

	for _, member := range content.Entries() {
		mediaType := member.Key.Str()
		// Resolved, because 3.2 lets a `content` entry reference a Media Type
		// Object kept in `components.mediaTypes`. An unresolved reference here
		// yields a message with no body and no schema, which is indistinguishable
		// from a media type whose author gave it neither.
		mediaTypeObject := d.Resolve(member.Value)
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
				if value, ok := d.generateValueFor(schema, nil, dir); ok {
					if body, ok := renderBody(value, mediaType); ok {
						msg.body, msg.hasBody = body, true
					}
				}
			}

			// A multipart or form-encoded schema describes the FIELDS rather
			// than a JSON document, so it is carried for generation — which
			// builds the fields from it — but a JSON body is the only thing a
			// JSON Schema can be validated against.
			if withSchema && schema.Valid() &&
				(isJSONMediaType(mediaType) || isMultipartMediaType(mediaType) || isFormMediaType(mediaType)) {
				if converted, ok := d.convertSchemaFor(schema, dir); ok {
					msg.schema = converted
				}
			}

			messages = append(messages, msg)
		}
	}

	return messages
}

// exampleValue reads an Example Object's example as data.
//
// 3.2 split the old `value` into two fields, because `value` never said which of
// the two things it was: a string example of a JSON body might be the string or
// the serialisation of it. `dataValue` is the half that is unambiguously data,
// and is therefore read exactly as `value` was — the schema validates it and the
// media type serialises it. Reading only `value` would leave a document written
// the new way sending generated bodies while its author's examples sat unused.
//
// `serializedValue` is the other half and is not read: the bytes are already
// serialised, so using them means parsing the media type back into data before
// anything can be compared. It is reported as unsupported instead.
func exampleValue(example node) node {
	if value := example.Get("value"); value.Valid() {
		return value
	}
	return example.Get("dataValue")
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

		value := exampleValue(example)
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
