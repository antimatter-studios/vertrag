package openapi3

import "regexp"

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

// parseContent turns a Content Object into one message per media type.
//
// withSchema controls whether a JSON Schema is attached. It is emitted for
// responses only: it exists so a consumer can validate what came back, and
// there is nothing to validate on the way out.
func (d *document) parseContent(content node, withSchema bool) []message {
	var messages []message

	for _, member := range content.Entries() {
		mediaType := member.Key.Str()
		mediaTypeObject := member.Value

		msg := message{contentType: mediaType}

		// An explicit example is what the author chose to demonstrate, and
		// takes precedence over anything derived from the schema.
		if example := mediaTypeObject.Get("example"); example.Valid() {
			if body, ok := renderBody(scalarValue(example), mediaType); ok {
				msg.body, msg.hasBody = body, true
			}
		} else if example, ok := d.firstNamedExample(mediaTypeObject.Get("examples")); ok {
			if body, ok := renderBody(example, mediaType); ok {
				msg.body, msg.hasBody = body, true
			}
		}

		schema := mediaTypeObject.Get("schema")
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

	return messages
}

// firstNamedExample takes the first entry of an Examples Object.
//
// Only the first is used, because a transaction carries one body; the reference
// makes the same choice and warns about the rest.
func (d *document) firstNamedExample(examples node) (any, bool) {
	for _, member := range examples.Entries() {
		example := d.Resolve(member.Value)
		if value := example.Get("value"); value.Valid() {
			return scalarValue(value), true
		}
		return nil, false
	}
	return nil, false
}
