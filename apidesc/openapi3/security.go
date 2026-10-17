package openapi3

import (
	"github.com/antimatter-studios/vertrag/refract"
)

// parseSecurity reads what an operation requires a caller to prove.
//
// The schemes themselves live in components; an operation names them and the
// document defines them, so the two have to be read together to say anything
// useful. What comes out is the practical question a run needs answered: which
// credential is wanted, where does it travel, and — for the authorization
// phase — which roles it has to carry.
//
// The shape is kept rather than flattened, because the shape is the meaning.
// `security` is a list of Security Requirement Objects, any ONE of which lets
// a caller in, and each object names schemes that must ALL be satisfied. The
// array against each scheme is its OAuth2 scopes, or in OpenAPI 3.1 the role
// names any other kind of scheme requires. A flat list of the schemes
// mentioned — what this used to produce — is enough to say which credential is
// missing and loses everything needed to say who may call what.
//
// An operation declaring `security: []` overrides the document's default and
// requires nothing, which is why an empty list is distinct from an absent key:
// the first is a container marked public, the second inherits the document's
// requirement, and an operation with neither has no container at all. An empty
// object among the alternatives, `{}`, is the same promise made optionally —
// a caller with no credential is let in too — and is marked public as well.
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
	public := len(requirements.Items()) == 0

	for _, object := range requirements.Items() {
		if !object.IsMapping() {
			continue
		}
		if len(object.Entries()) == 0 {
			public = true
			continue
		}

		requirement := refract.Named("securityRequirement")
		satisfiable := true
		for _, member := range object.Entries() {
			name := member.Key.Str()
			scheme := d.Resolve(schemes.Get(name))
			if !scheme.IsMapping() {
				// Named but never defined. The document is inconsistent, and
				// no caller can satisfy a requirement naming a scheme nobody
				// can say how to present — so the whole alternative goes, not
				// only this entry. Dropping the entry alone would turn
				// `{Undefined: [], Bearer: [admin]}` into a requirement that
				// a bearer token on its own satisfies.
				satisfiable = false
				break
			}

			element := refract.Named("securityScheme")
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

			var roles []*refract.Element
			for _, role := range member.Value.Items() {
				if text := role.Str(); text != "" {
					roles = append(roles, refract.String(text))
				}
			}
			if len(roles) > 0 {
				element.SetAttr("roles", refract.Array(roles...))
			}

			requirement.Append(element)
		}
		if satisfiable {
			container.Append(requirement)
		}
	}

	if public {
		container.SetAttr("public", refract.Bool(true))
	}
	return container
}
