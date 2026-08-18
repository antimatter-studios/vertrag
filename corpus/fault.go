package corpus

// A Fault is one way the server can deviate from what its description promises.
//
// Each exists to be found. A contract tester is only as good as the violations
// it notices, and the way to establish that it notices one is to commit the
// violation deliberately and check it is reported — and, just as important,
// that nothing else is. A fault that produces two findings is telling you the
// tool cannot separate causes.
//
// They are individually switchable rather than bundled into a "broken server",
// because a server with several faults at once cannot tell you which finding
// came from which, and that is the only question worth asking.
type Fault string

const (
	// FaultWrongStatus answers 500 where the description promises success.
	// The simplest violation there is, and the one every tester must catch.
	FaultWrongStatus Fault = "wrong-status"

	// FaultWrongContentType sends text/plain where JSON was promised, with a
	// body that is still valid JSON. A tester comparing only the body passes
	// this, which is why the check exists separately.
	FaultWrongContentType Fault = "wrong-content-type"

	// FaultBodyViolatesSchema returns a body of the right shape whose values
	// break the schema's constraints — a name past its maxLength, an id below
	// its minimum. It passes any check that verifies only which properties are
	// present, which is what Dredd does without a schema.
	FaultBodyViolatesSchema Fault = "body-violates-schema"

	// FaultMissingProperty omits a property the response schema requires.
	FaultMissingProperty Fault = "missing-property"

	// FaultMissingHeader omits a header the description declares.
	FaultMissingHeader Fault = "missing-header"

	// FaultHeaderViolatesSchema sends a declared header whose value breaks its
	// schema — text where an integer was promised. Nothing but vertrag's
	// opt-in header check looks at this, so it is the fault that distinguishes
	// that check from doing nothing.
	FaultHeaderViolatesSchema Fault = "header-violates-schema"

	// FaultAcceptsAnyParameter skips every parameter constraint, accepting
	// values the description forbids. Invisible to a run that sends only the
	// documented example, because that example is always valid — it is found
	// by generation or not at all.
	FaultAcceptsAnyParameter Fault = "accepts-any-parameter"

	// FaultAcceptsAnyBody is the same omission for the request body.
	FaultAcceptsAnyBody Fault = "accepts-any-body"

	// FaultRejectsValidInput refuses input the description permits, which is
	// the disagreement running the other way: the server is stricter than what
	// it published, so a client obeying the document is turned away.
	FaultRejectsValidInput Fault = "rejects-valid-input"

	// FaultCrashesOnBadInput answers 5xx instead of 4xx for input it should
	// simply refuse. Refusing input is a decision; failing on it is not, and
	// the difference matters because one is a validation gap and the other is
	// reachable from outside.
	FaultCrashesOnBadInput Fault = "crashes-on-bad-input"

	// FaultResourceLingersAfterDelete keeps serving a resource the server
	// said it had deleted. A read after a successful DELETE should not find
	// it; that it does is a use-after-free in the API's own terms, and no
	// single request can reveal it — only a sequence can, which is why it
	// exists here and not in the catalogue above.
	FaultResourceLingersAfterDelete Fault = "resource-lingers-after-delete"

	// FaultCreatedResourceMissing answers a creation with success and then
	// cannot find what it claims to have created. The create passes on its
	// own, the read passes on its own against some other identifier, and only
	// following the link the description declares between them shows that the
	// two disagree.
	FaultCreatedResourceMissing Fault = "created-resource-missing"
)

// Faults lists every fault, which is what lets a test assert it has considered
// all of them rather than the handful someone remembered.
func Faults() []Fault {
	return []Fault{
		FaultWrongStatus,
		FaultWrongContentType,
		FaultBodyViolatesSchema,
		FaultMissingProperty,
		FaultMissingHeader,
		FaultHeaderViolatesSchema,
		FaultAcceptsAnyParameter,
		FaultAcceptsAnyBody,
		FaultRejectsValidInput,
		FaultCrashesOnBadInput,
		FaultResourceLingersAfterDelete,
		FaultCreatedResourceMissing,
	}
}

// faultSet is the set of faults a server is running with.
type faultSet map[Fault]bool

func (f faultSet) has(fault Fault) bool { return f[fault] }
