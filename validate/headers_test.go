package validate

import (
	"encoding/json"
	"strings"
	"testing"
)

// headerFindings runs one header against one schema, which is the shape of
// every case here.
func headerFindings(t *testing.T, schema, value string) []string {
	t.Helper()
	if !json.Valid([]byte(schema)) {
		t.Fatalf("the test's own schema is not JSON: %s", schema)
	}
	return AgainstHeaderSchemas(
		map[string]json.RawMessage{"X-Thing": json.RawMessage(schema)},
		map[string]string{"x-thing": value},
	)
}

// TestAHeaderIsReadBackAsItsDeclaredTypeBeforeTheSchemaIsApplied pins the single
// decision this whole check rests on.
//
// A header is text, and "42" is not a JSON integer. Applying the schema to the
// raw string would therefore fail every correct server that has ever sent a
// numeric header — the exact false failure that makes people delete a contract
// test rather than trust it. The value is decoded as the type the schema
// declares first, and only then judged.
func TestAHeaderIsReadBackAsItsDeclaredTypeBeforeTheSchemaIsApplied(t *testing.T) {
	if findings := headerFindings(t, `{"type":"integer","minimum":0}`, "42"); len(findings) != 0 {
		t.Errorf("a correct integer header must not be reported, got %v", findings)
	}

	// And the constraint still bites once the value is a number.
	findings := headerFindings(t, `{"type":"integer","minimum":0}`, "-1")
	if len(findings) != 1 || !strings.Contains(findings[0], "X-Thing") {
		t.Fatalf("a value below the minimum should be reported, got %v", findings)
	}
}

// TestAHeaderThatCannotBeItsDeclaredTypeIsReported pins the case the check was
// built for: a server sending nonsense where the description promised a number.
// Dredd passes this, because it never looks at a header's value.
func TestAHeaderThatCannotBeItsDeclaredTypeIsReported(t *testing.T) {
	findings := headerFindings(t, `{"type":"integer"}`, "banana")
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want one", findings)
	}
	if !strings.Contains(findings[0], "an integer") || !strings.Contains(findings[0], `"banana"`) {
		t.Errorf("the finding should name the value and the promised type, got %q", findings[0])
	}
}

// TestASchemaThisCannotReadOffTheWireChecksNothing pins the conservative half of
// the design.
//
// Each of these describes a value whose text form is either ambiguous or not
// specified by the `simple` style at all. Guessing at one would fail servers
// behaving exactly as documented, which is a worse outcome than the check
// staying silent — so silence is what it does.
func TestASchemaThisCannotReadOffTheWireChecksNothing(t *testing.T) {
	unreadable := map[string]string{
		"an object, whose simple-style rendering almost nothing implements":     `{"type":"object","required":["a"]}`,
		"a schema with no type, where there is nothing to decode towards":       `{"pattern":"^[0-9]+$"}`,
		"a composition, which says nothing about how the value is written":      `{"allOf":[{"type":"integer"}]}`,
		"a 3.1 type list with more than one real type, genuinely ambiguous":     `{"type":["integer","string"]}`,
		"a list of objects, which the comma convention cannot separate":         `{"type":"array","items":{"type":"object"}}`,
		"a list with no items at all, leaving the separated pieces undescribed": `{"type":"array"}`,
	}

	for reason, schema := range unreadable {
		if findings := headerFindings(t, schema, "banana"); len(findings) != 0 {
			t.Errorf("%s: expected silence, got %v", reason, findings)
		}
	}
}

// TestAMissingHeaderIsNotReportedHere records where that failure comes from
// instead. Gavel already demands every header a description declares, so an
// absent one fails through ordinary validation; reporting it a second time would
// put the same problem in a report twice.
func TestAMissingHeaderIsNotReportedHere(t *testing.T) {
	findings := AgainstHeaderSchemas(
		map[string]json.RawMessage{"X-Thing": json.RawMessage(`{"type":"integer"}`)},
		map[string]string{"content-type": "application/json"},
	)
	if len(findings) != 0 {
		t.Errorf("presence belongs to ordinary validation, got %v", findings)
	}

	expected := Message{
		StatusCode: "200",
		Headers:    map[string]string{"X-Thing": ""},
	}
	actual := Message{StatusCode: "200", Headers: map[string]string{}}
	if Validate(expected, actual).Valid {
		t.Error("a declared header the response omits must still fail ordinary validation")
	}
}

// TestAListHeaderIsSplitOnTheComma pins the one non-primitive form `simple`
// style renders unambiguously, including the surrounding space HTTP allows
// around the separator — `a, b` is two items, not one item and one that begins
// with a space.
func TestAListHeaderIsSplitOnTheComma(t *testing.T) {
	const schema = `{"type":"array","items":{"type":"integer"},"maxItems":2}`

	if findings := headerFindings(t, schema, "1, 2"); len(findings) != 0 {
		t.Errorf("a two-item list must satisfy maxItems 2, got %v", findings)
	}
	if findings := headerFindings(t, schema, "1,2,3"); len(findings) != 1 {
		t.Errorf("a three-item list should break maxItems 2, got %v", findings)
	}
	if findings := headerFindings(t, schema, "1,banana"); len(findings) != 1 {
		t.Errorf("an item that is not an integer should be reported, got %v", findings)
	}
}

// TestAnEmptyListHeaderIsEmptyRatherThanOneEmptyValue pins a false failure that
// splitting naively produces: strings.Split("", ",") yields one empty element,
// which would break both `minItems: 0` reasoning and the item type, for a server
// that correctly sent an empty list.
func TestAnEmptyListHeaderIsEmptyRatherThanOneEmptyValue(t *testing.T) {
	findings := headerFindings(t, `{"type":"array","items":{"type":"integer"}}`, "")
	if len(findings) != 0 {
		t.Errorf("an empty list header must not be reported, got %v", findings)
	}
}

// TestABooleanHeaderIsReadWithoutRegardToCase records a deliberate leniency: a
// server sending `True` has communicated the value perfectly, and failing it
// would be a finding nobody can act on.
func TestABooleanHeaderIsReadWithoutRegardToCase(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "False"} {
		if findings := headerFindings(t, `{"type":"boolean"}`, value); len(findings) != 0 {
			t.Errorf("%q should read as a boolean, got %v", value, findings)
		}
	}
	if findings := headerFindings(t, `{"type":"boolean"}`, "yes"); len(findings) != 1 {
		t.Errorf("`yes` is not how a boolean is written, got %v", findings)
	}
}

// TestAStringHeaderIsJudgedOnExactlyTheBytesSent pins that a string type is the
// one case with no decoding at all, so `pattern` and `enum` apply to what the
// server really sent rather than to something trimmed or reinterpreted.
func TestAStringHeaderIsJudgedOnExactlyTheBytesSent(t *testing.T) {
	if findings := headerFindings(t, `{"type":"string","pattern":"^v[0-9]+$"}`, "v2"); len(findings) != 0 {
		t.Errorf("a matching value must not be reported, got %v", findings)
	}
	if findings := headerFindings(t, `{"type":"string","pattern":"^v[0-9]+$"}`, " v2"); len(findings) != 1 {
		t.Errorf("a leading space breaks the pattern and is the server's doing, got %v", findings)
	}
	if findings := headerFindings(t, `{"type":"string","enum":["a","b"]}`, "c"); len(findings) != 1 {
		t.Errorf("a value outside the enum should be reported, got %v", findings)
	}
}

// TestFormatIsAssertedOnAHeaderExactlyAsOnABody pins that a header's `format`
// carries the same weight it already carries inside a response body.
//
// Under draft-04, which is what an OpenAPI 3.0 document yields, format is an
// assertion rather than an annotation, and vertrag has always enforced it on
// bodies. Exempting headers would make one keyword mean two different things
// within a single description — a reader would have no way to tell which half of
// their document was being checked.
func TestFormatIsAssertedOnAHeaderExactlyAsOnABody(t *testing.T) {
	if findings := headerFindings(t, `{"type":"string","format":"uuid"}`,
		"5b1e1a5c-3f5a-4b1e-9c1a-2f5a4b1e9c1a"); len(findings) != 0 {
		t.Errorf("a well-formed uuid must not be reported, got %v", findings)
	}
	if findings := headerFindings(t, `{"type":"string","format":"uuid"}`, "not-a-uuid"); len(findings) != 1 {
		t.Errorf("a malformed uuid should be reported, got %v", findings)
	}
}

// TestHeaderNamesAreMatchedWithoutRegardToCase pins the join between the
// document's spelling and the wire's: descriptions write `X-Rate-Limit` and the
// runner records what arrived in lower case.
func TestHeaderNamesAreMatchedWithoutRegardToCase(t *testing.T) {
	findings := AgainstHeaderSchemas(
		map[string]json.RawMessage{"X-Rate-Limit": json.RawMessage(`{"type":"integer"}`)},
		map[string]string{"x-rate-limit": "banana"},
	)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want one", findings)
	}
	if !strings.Contains(findings[0], "X-Rate-Limit") {
		t.Errorf("the finding should use the document's spelling, got %q", findings[0])
	}
}

// TestFindingsAreOrderedByHeaderName keeps two runs of the same failure reading
// the same way, which map iteration would not.
func TestFindingsAreOrderedByHeaderName(t *testing.T) {
	schemas := map[string]json.RawMessage{
		"X-B": json.RawMessage(`{"type":"integer"}`),
		"X-A": json.RawMessage(`{"type":"integer"}`),
	}
	headers := map[string]string{"x-a": "banana", "x-b": "banana"}

	for i := 0; i < 20; i++ {
		findings := AgainstHeaderSchemas(schemas, headers)
		if len(findings) != 2 || !strings.Contains(findings[0], "X-A") {
			t.Fatalf("findings = %v, want X-A first", findings)
		}
	}
}

// TestAnUnreadableSchemaChecksNothing pins that a payload vertrag itself failed
// to produce properly cannot fail a server: the fault would be here, and the
// finding would send the reader looking in the wrong place entirely.
func TestAnUnreadableSchemaChecksNothing(t *testing.T) {
	findings := AgainstHeaderSchemas(
		map[string]json.RawMessage{"X-Thing": json.RawMessage(`{not json`)},
		map[string]string{"x-thing": "banana"},
	)
	if len(findings) != 0 {
		t.Errorf("a broken schema says nothing about the server, got %v", findings)
	}
}
