package fuzz

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// ValidPins replace a generated value with a usable one on one operation, in
// valid-mode requests only, keyed by operationId and then by field.
//
// They exist for the business rule a description cannot carry. A password rule
// can refuse the generator's `xxxxxxxxxxxx` where the schema promises success,
// and neither of the older answers keeps that probe. A global pin reaches every
// operation with a field of that name, whatever the field means there, and a
// hook that rewrites the body ends the probe, because what was sent is no longer
// what was drawn.
//
// Applied at the same point as a pin, before the request is rendered, the value
// is what was drawn as far as everything after it can tell: the probe is sent,
// judged and reported as usual.
//
// It differs from Pins in one way, and that is why it is a separate setting. A
// pin holds its field in every request, because it is a safety interlock. These
// leave invalid-mode requests alone: holding the field there would make its
// invalid values valid, and the probes that a too-short password is refused
// would be abandoned without being sent. A field that must never vary belongs
// in fuzz.pin, which is applied afterwards and so always wins.
type ValidPins map[string]Pins

// Operations lists the configured operationIds in a stable order.
func (v ValidPins) Operations() []string {
	operations := make([]string, 0, len(v))
	for operation := range v {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	return operations
}

// Names lists every configured field as `operation.field`, the form the run
// reports them in. Only names: these values are often passwords, so a run's
// output does not repeat them.
func (v ValidPins) Names() []string {
	var names []string
	for _, operation := range v.Operations() {
		for _, field := range v[operation].Names() {
			names = append(names, operation+"."+field)
		}
	}
	return names
}

// PinTarget is what one transaction lets a valid pin hold: the operation it
// belongs to, its body schemas and its GraphQL arguments with their schemas.
type PinTarget struct {
	Operation string
	Bodies    []generate.Schema
	Arguments map[string]generate.Schema
}

// CheckValidPins refuses a valid pin that could only ever do nothing, before
// the first request goes out. An operation or field that does not exist is the
// typo fuzz.pin refuses too. A value the field's own schema forbids is the case
// peculiar to these: the probe's claim to be valid would no longer hold, so it
// would be abandoned unsent, which is the loss this setting exists to end.
func CheckValidPins(pins ValidPins, targets []PinTarget) error {
	var problems []string
	for _, operation := range pins.Operations() {
		var own []PinTarget
		for _, target := range targets {
			if target.Operation == operation {
				own = append(own, target)
			}
		}
		if len(own) == 0 {
			problems = append(problems, fmt.Sprintf("%s: no transaction in this run has that operationId", operation))
			continue
		}
		for _, field := range pins[operation].Names() {
			schemas := fieldSchemas(own, field)
			if len(schemas) == 0 {
				problems = append(problems, fmt.Sprintf("%s.%s: the operation declares no such field", operation, field))
				continue
			}
			if reason := refusedBy(schemas, pins[operation][field]); reason != "" {
				problems = append(problems, fmt.Sprintf("%s.%s: %s", operation, field, reason))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("fuzz.pin-valid cannot hold what it names: %s", strings.Join(problems, "; "))
}

// fieldSchemas finds the schemas a field has in an operation's transactions.
func fieldSchemas(targets []PinTarget, field string) []any {
	var schemas []any
	for _, target := range targets {
		if schema, ok := target.Arguments[field]; ok {
			schemas = append(schemas, map[string]any(schema))
		}
		for _, body := range target.Bodies {
			if properties, ok := body["properties"].(map[string]any); ok {
				if schema, declared := properties[field]; declared {
					schemas = append(schemas, schema)
				}
			}
		}
	}
	return schemas
}

// refusedBy says why a value does not satisfy every schema, or nothing if it
// does.
func refusedBy(schemas []any, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "the value cannot be written as JSON"
	}
	for _, schema := range schemas {
		raw, err := json.Marshal(schema)
		if err != nil {
			continue
		}
		if !validate.AgainstSchema(raw, string(encoded)).Valid {
			return "the value does not satisfy the field's schema, so its probes would never be sent"
		}
	}
	return ""
}

// hold applies what is held fixed to one drawn value, and counts it. Every path
// from a draw to the wire goes through here. The valid pin comes first so that a
// fuzz.pin on the same field always wins.
func (o Options) hold(subject Subject, schema generate.Schema, mode generate.Mode, value any) any {
	if mode == generate.Valid {
		if pins := o.ValidPins[o.Operation]; len(pins) > 0 {
			held, engaged := pins.ApplyTo(subject, schema, value)
			value = held
			for _, name := range engaged {
				if o.Engaged != nil {
					o.Engaged[o.Operation+"."+name]++
				}
			}
		}
	}
	held, engaged := o.Pin.ApplyTo(subject, schema, value)
	for _, name := range engaged {
		if o.Engaged != nil {
			o.Engaged[name]++
		}
	}
	return held
}
