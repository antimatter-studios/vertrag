package openapi3

import (
	"github.com/antimatter-studios/vertrag/refract"
)

// parseLinks reads a Response Object's Links Object.
//
// A link says that this response leads to another operation, and how to fill
// that operation's parameters from what just came back. It is the only thing in
// OpenAPI that describes a sequence, and Dredd's parser marks it unsupported —
// so an API whose whole behaviour is "create, then read what you created" can
// only be tested one isolated request at a time.
//
// The links ride as a refract attribute on the httpResponse. Refract is an
// extensible element model and an element named `link` cannot collide with
// anything Dredd emits, which matters because the compile oracle feeds Dredd's
// own API Elements through the same compiler: those carry no links attribute,
// and the compiler has to tolerate its absence, which it does for free.
func (d *document) parseLinks(links node) *refract.Element {
	if !links.IsMapping() {
		return nil
	}

	container := refract.Named("links")
	found := false

	for _, member := range links.Entries() {
		link := d.Resolve(member.Value)
		if !link.IsMapping() {
			continue
		}

		element := refract.Named("link")
		element.SetAttr("name", refract.String(member.Key.Str()))

		// A link names its target either by identifier or by reference. The
		// identifier is the common form and the only one a description can use
		// without knowing its own URL.
		if id := link.Get("operationId").Str(); id != "" {
			element.SetAttr("operationId", refract.String(id))
		}
		if ref := link.Get("operationRef").Str(); ref != "" {
			element.SetAttr("operationRef", refract.String(ref))
		}

		if parameters := d.parseLinkParameters(link.Get("parameters")); parameters != nil {
			element.SetAttr("parameters", parameters)
		}
		if body := link.Get("requestBody"); body.IsScalar() {
			element.SetAttr("requestBody", refract.String(body.Str()))
		}

		container.Append(element)
		found = true
	}

	if !found {
		return nil
	}
	return container
}

// parseLinkParameters reads the map of parameter name to runtime expression.
//
// A key may be bare — `userId` — or qualified with the location the value is
// for — `path.userId`. The qualified form exists because one operation can
// legitimately have a query and a path parameter of the same name, and the
// unqualified form cannot say which is meant.
func (d *document) parseLinkParameters(parameters node) *refract.Element {
	if !parameters.IsMapping() {
		return nil
	}

	out := refract.Named("linkParameters")
	found := false
	for _, member := range parameters.Entries() {
		value := member.Value
		if !value.IsScalar() {
			// A parameter whose value is an object or a list is not a runtime
			// expression. There is nothing to evaluate, and inventing something
			// would send a value the description never asked for.
			continue
		}
		out.Append(refract.Member(member.Key.Str(), refract.String(value.Str())))
		found = true
	}

	if !found {
		return nil
	}
	return out
}
