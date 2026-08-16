package compile

import "github.com/antimatter-studios/vertrag/refract"

// Link is one OpenAPI Link Object, reduced to what a run needs.
//
// A link says that the response carrying it leads to another operation, and how
// to fill that operation's parameters from the exchange that just completed.
// It is the only construct in OpenAPI that describes a sequence.
type Link struct {
	// Name is the key the link was declared under, which is how a report names
	// it and the only human-readable handle a description gives.
	Name string

	// OperationID names the target operation. OperationRef is the alternative
	// spelling, a JSON Reference to the operation, used by documents that give
	// their operations no identifiers.
	OperationID  string
	OperationRef string

	// Parameters maps a parameter of the target operation to a runtime
	// expression evaluated against the completed exchange. A key may be bare
	// or qualified with a location, as in `path.userId`, because one operation
	// can have a query and a path parameter of the same name.
	Parameters map[string]string

	// RequestBody is a runtime expression for the target's whole body.
	RequestBody string
}

// compileLinks reads the links attribute off an httpResponse element.
//
// A response with no links attribute yields none. That is the ordinary case and
// also what every API Elements document Dredd produces looks like, which is
// what lets the compile oracle keep feeding Dredd's own fixtures through here.
func compileLinks(element *refract.Element) []Link {
	if element == nil {
		return nil
	}
	container := element.Attr("links")
	if container == nil {
		return nil
	}

	var links []Link
	for _, child := range container.ContentChildren() {
		if child.Name != "link" {
			continue
		}

		link := Link{
			Name:         child.Attr("name").String(),
			OperationID:  child.Attr("operationId").String(),
			OperationRef: child.Attr("operationRef").String(),
			RequestBody:  child.Attr("requestBody").String(),
		}

		if parameters := child.Attr("parameters"); parameters != nil {
			link.Parameters = map[string]string{}
			for _, member := range parameters.ContentChildren() {
				if member.Kind != refract.ContentMember {
					continue
				}
				name, _ := member.Key.StringValue()
				link.Parameters[name] = member.Value.String()
			}
		}

		links = append(links, link)
	}
	return links
}
