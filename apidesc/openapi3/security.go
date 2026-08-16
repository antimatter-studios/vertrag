package openapi3

import (
	"github.com/antimatter-studios/vertrag/refract"
)

// parseSecurity reads what an operation requires a caller to prove.
//
// The schemes themselves live in components; an operation names them and the
// document defines them, so the two have to be read together to say anything
// useful. What comes out is the practical question a run needs answered: which
// credential is wanted, and where does it travel.
//
// An operation declaring `security: []` overrides the document's default and
// requires nothing, which is why an empty list is distinct from an absent key.
func (d *document) parseSecurity(operation node) *refract.Element {
	requirements := operation.Get("security")
	if !requirements.Valid() {
		// Falls back to the document's own default, if it declared one.
		requirements = d.Root.Get("security")
	}
	if !requirements.IsSequence() {
		return nil
	}

	schemes := d.Root.Get("components").Get("securitySchemes")

	container := refract.Named("security")
	found := false

	for _, requirement := range requirements.Items() {
		if !requirement.IsMapping() {
			continue
		}
		for _, member := range requirement.Entries() {
			name := member.Key.Str()
			scheme := d.Resolve(schemes.Get(name))
			if !scheme.IsMapping() {
				// Named but never defined. The document is inconsistent, and
				// pretending the requirement is satisfiable would hide that.
				continue
			}

			element := refract.Named("securityRequirement")
			element.SetAttr("name", refract.String(name))
			element.SetAttr("type", refract.String(scheme.Get("type").Str()))
			if in := scheme.Get("in").Str(); in != "" {
				element.SetAttr("in", refract.String(in))
			}
			if header := scheme.Get("name").Str(); header != "" {
				element.SetAttr("parameter", refract.String(header))
			}
			if s := scheme.Get("scheme").Str(); s != "" {
				element.SetAttr("scheme", refract.String(s))
			}

			container.Append(element)
			found = true
		}
	}

	if !found {
		return nil
	}
	return container
}
