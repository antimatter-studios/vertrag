package generate

// FromFormat produces a specimen for a JSON Schema `format`.
//
// `format` is assertive in the validator vertrag uses, so a schema declaring
// `format: uuid` and a specimen of "" disagree — and the specimen is what gets
// sent as a request body, so a server checking the format rejects it and the
// run reports the server as broken. Every value here is a real instance of its
// format rather than a plausible-looking string, because the validator will
// parse it.
//
// The formats are those JSON Schema defines plus the two OpenAPI adds. One it
// does not recognise yields nothing, and the caller falls back to a plain
// string: a format nobody validates constrains nothing, and inventing a value
// for it would be guessing at a convention that may not exist.
func FromFormat(format string) (string, bool) {
	switch format {
	case "date-time":
		// RFC 3339, which is what the validator parses. Not an HTTP date: a
		// description marking a Date header `date-time` is describing something
		// its own server cannot send, and that is the description's problem to
		// see rather than one to paper over here.
		return "2020-01-02T03:04:05Z", true
	case "date":
		return "2020-01-02", true
	case "time":
		return "03:04:05Z", true
	case "duration":
		return "P1D", true

	case "email", "idn-email":
		return "someone@example.com", true
	case "hostname", "idn-hostname":
		return "example.com", true

	case "ipv4":
		return "192.0.2.1", true
	case "ipv6":
		return "2001:db8::1", true

	case "uri", "iri":
		return "https://example.com/a", true
	case "uri-reference", "iri-reference":
		return "/a", true
	case "uri-template":
		return "https://example.com/{id}", true

	case "uuid":
		return "00000000-0000-4000-8000-000000000000", true

	case "json-pointer":
		return "/a", true
	case "relative-json-pointer":
		return "0", true
	case "regex":
		return "a", true

	// OpenAPI's own two. `byte` is base64 and `binary` is raw octets, and
	// neither is a JSON Schema format, but a description written against
	// OpenAPI uses them and a specimen has to satisfy anything asserting them.
	case "byte":
		return "dmVydHJhZw==", true
	case "binary":
		return "vertrag", true

	default:
		return "", false
	}
}

// The reserved ranges are used deliberately above. example.com, 192.0.2.0/24
// and 2001:db8::/32 exist precisely so that documentation and tests can name an
// address without naming someone's actual server — a generated specimen that
// escapes into a real request should reach nothing.
