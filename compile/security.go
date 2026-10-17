package compile

import "github.com/antimatter-studios/vertrag/refract"

// Security is what an operation requires a caller to prove, as the
// description states it.
//
// vertrag cannot invent a credential and does not try. Carrying the requirement
// is what lets a run say WHICH one is missing, instead of sending
// unauthenticated requests and reporting a wall of 401s that say nothing about
// the contract — and, for a scheme that does not travel in a header, that no
// mechanism exists to supply it at all. It is also what the authorization
// phase reads to decide who may call what.
type Security struct {
	// Declared is set when the description says anything about this
	// operation's security, on the operation itself or as the document's
	// default. An operation with nothing declared at either level is one the
	// description is silent about, which is not the same as one it calls
	// public: most descriptions that say nothing say nothing because nobody
	// wrote it down.
	Declared bool

	// Public is set when the description admits a caller with no credential:
	// `security: []` on the operation, or an empty object `{}` among its
	// alternatives. It is the override OpenAPI gives an operation over the
	// document's default, and nothing that probes for a missing credential
	// may expect a refusal from one.
	Public bool

	// Requirements are alternatives, any one of which lets a caller in.
	Requirements []Requirement
}

// Requirement is one Security Requirement Object: every scheme it names must
// be satisfied, each with the roles listed against it.
type Requirement []Need

// Need is one scheme a requirement names, and the roles a credential for it
// must carry.
//
// For OAuth2 and OpenID Connect the roles are scopes. For any other kind of
// scheme OpenAPI 3.0 required the list to be empty, and 3.1 lets it name the
// roles "required for the execution" — which is how a description says that a
// bearer token is not enough, it has to be an administrator's.
type Need struct {
	Scheme SecurityScheme
	Roles  []string
}

// Schemes are every scheme the requirements mention, once each, in the order
// the description first names them.
func (s Security) Schemes() []SecurityScheme {
	seen := map[string]bool{}
	var out []SecurityScheme
	for _, requirement := range s.Requirements {
		for _, need := range requirement {
			if seen[need.Scheme.Name] {
				continue
			}
			seen[need.Scheme.Name] = true
			out = append(out, need.Scheme)
		}
	}
	return out
}

// SecurityScheme is one credential an operation may require a caller to
// prove: how it is presented, as the description's components define it.
type SecurityScheme struct {
	// Name is the key the scheme was declared under in components.
	Name string

	// Type is the scheme's kind: apiKey, http, oauth2 or openIdConnect.
	Type string

	// In and Parameter say where an apiKey travels and what it is called.
	In        string
	Parameter string

	// Scheme is an http scheme's own name — bearer, basic and so on.
	Scheme string
}

// Supplier describes how a credential can be given to a run, which is the
// actionable half of reporting one missing.
func (s SecurityScheme) Supplier() string {
	switch {
	case s.Type == "apiKey" && s.In == "header":
		return "--header '" + s.Parameter + ": <value>'"
	case s.Type == "apiKey" && s.In == "cookie":
		// This used to be grouped with the query key below, as something no
		// flag could supply. It is not, now that a run merges Cookie headers
		// rather than replacing them: the credential joins whatever cookies
		// the description already declares instead of wiping them out, which
		// is what made the advice unusable before.
		return "--header 'Cookie: " + s.Parameter + "=<value>'"
	case s.Type == "http" && s.Scheme == "bearer":
		return "--header 'Authorization: Bearer <token>'"
	case s.Type == "http" && s.Scheme == "basic":
		return "--header 'Authorization: Basic <base64>'"
	case s.Type == "apiKey":
		// A key travelling in the query cannot be supplied by --header, and
		// saying "use --header" would send someone in circles.
		return "no flag supplies a credential in the " + s.In + "; a hook can set it"
	default:
		return "a hook can set the credential this scheme needs"
	}
}

func compileSecurity(element *refract.Element) Security {
	if element == nil {
		return Security{}
	}
	container := element.Attr("security")
	if container == nil {
		return Security{}
	}

	security := Security{Declared: true}
	if public := container.Attr("public"); public != nil {
		security.Public, _ = public.Primitive.(bool)
	}
	for _, child := range container.ContentChildren() {
		if child.Name != "securityRequirement" {
			continue
		}
		var requirement Requirement
		for _, scheme := range child.ContentChildren() {
			if scheme.Name != "securityScheme" {
				continue
			}
			need := Need{Scheme: SecurityScheme{
				Name:      scheme.Attr("name").String(),
				Type:      scheme.Attr("type").String(),
				In:        scheme.Attr("in").String(),
				Parameter: scheme.Attr("parameter").String(),
				Scheme:    scheme.Attr("scheme").String(),
			}}
			if roles := scheme.Attr("roles"); roles != nil {
				for _, role := range roles.ContentChildren() {
					if text := role.String(); text != "" {
						need.Roles = append(need.Roles, text)
					}
				}
			}
			requirement = append(requirement, need)
		}
		if len(requirement) > 0 {
			security.Requirements = append(security.Requirements, requirement)
		}
	}
	return security
}
