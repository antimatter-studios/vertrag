package compile

import "github.com/antimatter-studios/vertrag/refract"

// Security is one credential an operation requires a caller to prove.
//
// vertrag cannot invent a credential and does not try. Carrying the requirement
// is what lets a run say WHICH one is missing, instead of sending
// unauthenticated requests and reporting a wall of 401s that say nothing about
// the contract — and, for a scheme that does not travel in a header, that no
// mechanism exists to supply it at all.
type Security struct {
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
func (s Security) Supplier() string {
	switch {
	case s.Type == "apiKey" && s.In == "header":
		return "--header '" + s.Parameter + ": <value>'"
	case s.Type == "http" && s.Scheme == "bearer":
		return "--header 'Authorization: Bearer <token>'"
	case s.Type == "http" && s.Scheme == "basic":
		return "--header 'Authorization: Basic <base64>'"
	case s.Type == "apiKey":
		// A key travelling in the query or a cookie cannot be supplied by
		// --header, and saying "use --header" would send someone in circles.
		return "no flag supplies a credential in the " + s.In + "; a hook can set it"
	default:
		return "a hook can set the credential this scheme needs"
	}
}

func compileSecurity(element *refract.Element) []Security {
	if element == nil {
		return nil
	}
	container := element.Attr("security")
	if container == nil {
		return nil
	}

	var out []Security
	for _, child := range container.ContentChildren() {
		if child.Name != "securityRequirement" {
			continue
		}
		out = append(out, Security{
			Name:      child.Attr("name").String(),
			Type:      child.Attr("type").String(),
			In:        child.Attr("in").String(),
			Parameter: child.Attr("parameter").String(),
			Scheme:    child.Attr("scheme").String(),
		})
	}
	return out
}
