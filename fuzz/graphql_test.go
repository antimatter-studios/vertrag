package fuzz

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// A GraphQL endpoint answers 200 to its own refusals, so a judge reading the
// status alone would call every refusal an acceptance — and report a validation
// bypass for each one against a server doing exactly the right thing.
func TestAGraphQLRefusalIsReadFromTheBodyRatherThanTheStatus(t *testing.T) {
	subject := Subject{In: InArgument, Name: "size", Where: "count"}
	refusal := validate.Message{StatusCode: "200",
		Body: `{"errors":[{"message":"Int cannot represent non-integer value"}],"data":null}`}

	if _, bad := judge(generate.Invalid, subject, refusal); bad {
		t.Error("a refusal stated in the body was read as an acceptance")
	}

	accepted := validate.Message{StatusCode: "200", Body: `{"data":{"count":3}}`}
	message, bad := judge(generate.Invalid, subject, accepted)
	if !bad {
		t.Fatal("a value the schema forbids was answered without error and reported as nothing")
	}
	if !strings.Contains(message, `argument "size" of count`) {
		t.Errorf("the finding does not name the argument: %s", message)
	}
}

// A valid value the server refuses is a disagreement between the server and its
// own schema, and the server's own message is the diagnostic.
func TestAServerRefusingAValueItsSchemaPermitsIsAFinding(t *testing.T) {
	subject := Subject{In: InArgument, Name: "term", Where: "search"}
	reply := validate.Message{StatusCode: "200",
		Body: `{"errors":[{"message":"term must not contain spaces"}]}`}

	message, bad := judge(generate.Valid, subject, reply)
	if !bad {
		t.Fatal("a refusal of a permitted value was not reported")
	}
	if !strings.Contains(message, "term must not contain spaces") {
		t.Errorf("the server's own explanation is missing: %s", message)
	}
}

// Generation can produce anything the caller must SHAPE and nothing they must
// POSSESS. A generated id is well formed by construction and names nothing by
// luck, so a server saying so is right — the same exemption a path parameter's
// 404 already gets.
func TestAGeneratedIdentifierThatNamesNothingIsNotAFinding(t *testing.T) {
	subject := Subject{In: InArgument, Name: "id", Where: "user", Possessed: true}
	reply := validate.Message{StatusCode: "200",
		Body: `{"errors":[{"message":"no user with that id"}],"data":{"user":null}}`}

	if _, bad := judge(generate.Valid, subject, reply); bad {
		t.Error("a generated identifier resolving to nothing was reported as a finding")
	}

	// The invalid half keeps its teeth: a malformed value is malformed whatever
	// the caller holds, so a server accepting one has a bypass either way.
	accepted := validate.Message{StatusCode: "200", Body: `{"data":{"user":null}}`}
	if _, bad := judge(generate.Invalid, subject, accepted); !bad {
		t.Error("possession excused the invalid half as well, which it must not")
	}

	// And a 5xx is a finding whatever else is true: the server failed rather
	// than answered.
	broken := validate.Message{StatusCode: "500", Body: `{}`}
	if _, bad := judge(generate.Valid, subject, broken); !bad {
		t.Error("a 500 to a generated identifier was excused")
	}
}

// A pin names a FIELD of a generated body and an ARGUMENT by its own name. The
// second cannot be done by holding a property, because a GraphQL argument is
// the value rather than a member of one.
func TestAPinnedArgumentReplacesTheWholeDrawnValue(t *testing.T) {
	pins := Pins{"dryRun": true}
	subject := Subject{In: InArgument, Name: "dryRun", Where: "report"}
	schema := generate.Schema{"type": "boolean"}

	value, engaged := pins.ApplyTo(subject, schema, false)
	if value != true {
		t.Errorf("the drawn value was sent as %v, not as the pinned value", value)
	}
	if len(engaged) != 1 || engaged[0] != "dryRun" {
		t.Errorf("engaged = %v, want the pin reported as having held", engaged)
	}

	// An argument the pin does not name is left exactly as it was drawn.
	other := Subject{In: InArgument, Name: "term", Where: "search"}
	value, engaged = pins.ApplyTo(other, generate.Schema{"type": "string"}, "abc")
	if value != "abc" || len(engaged) != 0 {
		t.Errorf("a pin held an argument it does not name: %v %v", value, engaged)
	}
}

// A pin naming an argument no field declares must stop the run, exactly as one
// naming a body field none declares does. A GraphQL schema has no request body
// to declare a field in, so a check that looked only at bodies would refuse
// every pin ever written for a schema.
func TestAPinNamingAnArgumentTheSchemaDeclaresIsAccepted(t *testing.T) {
	if err := CheckPins(Pins{"dryRun": true}, nil, []string{"term", "dryRun"}); err != nil {
		t.Errorf("a pin naming a declared argument was refused: %v", err)
	}

	err := CheckPins(Pins{"dryrun": true}, nil, []string{"term", "dryRun"})
	if err == nil {
		t.Fatal("a pin naming no argument anywhere should refuse to run")
	}
	if !strings.Contains(err.Error(), "dryrun") {
		t.Errorf("the refusal does not name the typo: %v", err)
	}
}
