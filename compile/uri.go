package compile

import (
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/refract"
	"github.com/antimatter-studios/vertrag/uritemplate"
)

// param is one URI parameter gathered from an hrefVariables element.
//
// Presence is tracked separately from value because the rules distinguish "no
// example given" from "an example that happens to be empty", and the two lead
// to different diagnostics.
type param struct {
	name       string
	required   bool
	defaultVal any
	hasDefault bool
	example    any
	hasExample bool
	values     []any
	// schema is the JSON Schema a parser attached to the parameter, empty when
	// the description gave none or the elements came from a parser that does
	// not carry one. Nothing in URI expansion reads it; it is carried through
	// for generation.
	schema string
}

// paramSet preserves declaration order.
//
// Order is not cosmetic here: validation walks the set and appends a warning or
// error per offending parameter, so the order decides the order of annotations,
// which the oracle compares against the reference.
type paramSet struct {
	order  []string
	byName map[string]*param
}

func newParamSet() *paramSet {
	return &paramSet{byName: map[string]*param{}}
}

func (s *paramSet) get(name string) (*param, bool) {
	p, ok := s.byName[name]
	return p, ok
}

// set adds or replaces a parameter. A replaced parameter keeps its original
// position, matching JavaScript object semantics, where assigning to an
// existing key does not move it.
func (s *paramSet) set(p *param) {
	if _, exists := s.byName[p.name]; !exists {
		s.order = append(s.order, p.name)
	}
	s.byName[p.name] = p
}

func (s *paramSet) all() []*param {
	out := make([]*param, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.byName[name])
	}
	return out
}

// compileParams reads an hrefVariables element into an ordered parameter set.
func compileParams(hrefVariables *refract.Element) *paramSet {
	params := newParamSet()
	if hrefVariables == nil {
		return params
	}

	for _, member := range hrefVariables.ContentChildren() {
		if member.Kind != refract.ContentMember {
			continue
		}
		name, _ := member.Key.StringValue()
		value := member.Value

		p := &param{name: name, required: hasRequiredTypeAttribute(member)}

		if value != nil {
			p.schema = value.Attr(refract.SchemaAttribute).String()
			if def := value.Attr("default"); def != nil {
				p.defaultVal = def.ToValue()
				p.hasDefault = p.defaultVal != nil
			}
			p.values = enumerations(value)

			// An element with no content means the description gave no example.
			// The first enumeration then stands in for one, which is what makes
			// an enum parameter usable without an explicit example.
			if v := value.ToValue(); v != nil {
				p.example = v
				p.hasExample = true
			} else if len(p.values) > 0 {
				p.example = p.values[0]
				p.hasExample = p.values[0] != nil
			}
		}

		params.set(p)
	}
	return params
}

func hasRequiredTypeAttribute(member *refract.Element) bool {
	attrs := member.Attr("typeAttributes")
	if attrs == nil {
		return false
	}
	list, ok := attrs.ToValue().([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == "required" {
			return true
		}
	}
	return false
}

func enumerations(value *refract.Element) []any {
	attr := value.Attr("enumerations")
	if attr == nil {
		return nil
	}
	list, ok := attr.ToValue().([]any)
	if !ok {
		return nil
	}
	return list
}

// overrideParams layers one parameter set over another, later sets winning.
func overrideParams(base, overrides *paramSet) *paramSet {
	result := newParamSet()
	for _, p := range base.all() {
		result.set(p)
	}
	for _, p := range overrides.all() {
		result.set(p)
	}
	return result
}

// validateParams reports parameters that cannot produce a usable URI.
func validateParams(params *paramSet) (errs []string, warnings []string) {
	for _, p := range params.all() {
		if p.required && !isUsable(p.example, p.hasExample) && !isUsable(p.defaultVal, p.hasDefault) {
			errs = append(errs, fmt.Sprintf(
				"Required URI parameter '%s' has no example or default value.", p.name))
		}

		// The reference also switches on a `type` field here, rejecting values
		// that contradict a declared number or boolean type. That field is
		// never populated by its own parameter compilation, so the branch is
		// unreachable and is not ported; were it to become reachable, the
		// oracle would show up the difference as a missing annotation.

		if len(p.values) > 0 && !containsValue(p.values, p.example) {
			errs = append(errs, fmt.Sprintf(
				"URI parameter '%s' example value is not one of enum values.", p.name))
		}
	}
	return errs, warnings
}

// isUsable reports whether a value can stand in for a URI parameter: it must be
// present and not the empty string.
func isUsable(value any, present bool) bool {
	if !present || value == nil {
		return false
	}
	s, ok := value.(string)
	return !ok || s != ""
}

func containsValue(values []any, target any) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// compiledURI is a request's URI together with what it was built from.
//
// The template and the values are kept because generation replaces one
// parameter and expands again, which needs both. They say nothing the URI does
// not already contain for a run that only sends the compiled request.
type compiledURI struct {
	uri        string
	template   string
	parameters []Parameter
}

// compileURI resolves the URI for a request by cascading href and parameter
// declarations from the resource, through the transition, to the request.
func compileURI(request *refract.Element) (compiledURI, []Annotation) {
	var annotations []Annotation

	cascade := []*refract.Element{
		request.FindParent("resource"),
		request.FindParent("transition"),
		request,
	}

	// The last non-empty href wins, letting a transition or request narrow the
	// resource's template.
	var href string
	for _, element := range cascade {
		if element == nil {
			continue
		}
		if value := element.Attr("href").String(); value != "" {
			href = value
		}
	}

	params := newParamSet()
	for _, element := range cascade {
		if element == nil {
			continue
		}
		params = overrideParams(params, compileParams(element.Attr("hrefVariables")))
	}

	errs, warnings := validateParams(params)
	annotations = appendAnnotations(annotations, "parametersValidation", errs, warnings)

	uri, values, errs, warnings := expandURITemplate(href, params)
	annotations = appendAnnotations(annotations, "uriTemplateExpansion", errs, warnings)

	return compiledURI{
		uri:        uri,
		template:   href,
		parameters: uriParameters(href, params, values),
	}, annotations
}

// uriParameters describes the parameters the URI was expanded from.
//
// Where each one travels is read from the template rather than from the
// description: the operator is what decides it, so `{?limit}` is the query
// string and `{id}`, `{/id}` and `{.format}` are all the path. Asking the
// template also means a parameter the description declared and the template
// never mentions is left out, since there is nowhere in the request to put it.
func uriParameters(template string, params *paramSet, values map[string]any) []Parameter {
	parsed, err := uritemplate.Parse(template)
	if err != nil {
		return nil
	}

	var out []Parameter
	for _, expression := range parsed.Expressions {
		in := InPath
		if expression.Op == '?' || expression.Op == '&' {
			in = InQuery
		}
		for _, reference := range expression.Params {
			name := decodeURIComponentish(reference.Name)
			declared, ok := params.get(name)
			if !ok {
				continue
			}
			value, hasValue := values[name]
			out = append(out, Parameter{
				In:       in,
				Name:     name,
				Schema:   declared.schema,
				Value:    value,
				HasValue: hasValue,
			})
		}
	}
	return out
}

func appendAnnotations(annotations []Annotation, component string, errs, warnings []string) []Annotation {
	for _, message := range errs {
		annotations = append(annotations, Annotation{Type: "error", Component: component, Message: message})
	}
	for _, message := range warnings {
		annotations = append(annotations, Annotation{Type: "warning", Component: component, Message: message})
	}
	return annotations
}

// expandURITemplate fills a URI template from the compiled parameters.
//
// It returns an empty URI when the template cannot be expanded unambiguously.
// The caller treats that as "no request", so the transaction is dropped and
// only the annotations survive — a description that cannot yield a concrete
// URI produces a diagnostic rather than a guessed request.
//
// The values it expanded with are returned alongside, so that the same URI can
// be produced again with one of them changed.
func expandURITemplate(template string, params *paramSet) (uri string, values map[string]any, errs []string, warnings []string) {
	parsed, err := uritemplate.Parse(template)
	if err != nil {
		return "", nil, []string{fmt.Sprintf(
			"Failed to parse URI template: %s\nError: SyntaxError: %s", template, err)}, nil
	}

	var names []string
	for _, expr := range parsed.Expressions {
		for _, p := range expr.Params {
			names = append(names, decodeURIComponentish(p.Name))
		}
	}

	if len(parsed.Expressions) == 0 {
		return template, nil, nil, nil
	}

	ambiguous := false
	for _, name := range names {
		if _, ok := params.get(name); !ok {
			ambiguous = true
			warnings = append(warnings, fmt.Sprintf(
				"Ambiguous URI parameter in template: %s\nParameter not defined in API description document: %s",
				template, name))
		}
	}

	if ambiguous {
		return "", nil, errs, warnings
	}

	toExpand := map[string]any{}
	for _, name := range names {
		p, ok := params.get(name)
		if !ok {
			continue
		}
		switch {
		case isUsable(p.example, p.hasExample):
			toExpand[name] = p.example
		case isUsable(p.defaultVal, p.hasDefault):
			toExpand[name] = p.defaultVal
		case p.required:
			ambiguous = true
			warnings = append(warnings, fmt.Sprintf(
				"Ambiguous URI parameter in template: %s\nNo example value for required parameter in API description document: %s",
				template, name))
		}

		// A required parameter with a default is legal but meaningless: the
		// description is asking for a value it also says may be omitted.
		if p.required && isUsable(p.defaultVal, p.hasDefault) {
			warnings = append(warnings, fmt.Sprintf(
				"Required URI parameter '%s' has a default value.\nDefault value for a required parameter doesn't make sense from API description perspective. Use example value instead.",
				name))
		}
	}

	if ambiguous {
		return "", nil, errs, warnings
	}
	return parsed.Expand(toExpand), toExpand, errs, warnings
}

// decodeURIComponentish mirrors JavaScript's decodeURI: percent escapes are
// decoded except those standing for reserved characters, which stay escaped.
func decodeURIComponentish(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		hi, ok1 := hexValue(s[i+1])
		lo, ok2 := hexValue(s[i+2])
		if !ok1 || !ok2 {
			b.WriteByte(s[i])
			continue
		}
		decoded := byte(hi<<4 | lo)
		if strings.IndexByte(";/?:@&=+$,#", decoded) >= 0 {
			b.WriteString(s[i : i+3])
		} else {
			b.WriteByte(decoded)
		}
		i += 2
	}
	return b.String()
}

func hexValue(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}
