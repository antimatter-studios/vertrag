package openapi2

import "github.com/antimatter-studios/vertrag/yamldoc"

// The document reader is shared with the other format parser. These aliases
// keep this package reading in its own terms — a parser talks about nodes and
// entries, not about which package they came from.
type (
	node  = yamldoc.Node
	entry = yamldoc.Entry
	// orderedMap preserves key order, which matters because generated bodies
	// and schemas are compared as strings.
	orderedMap = yamldoc.OrderedMap
)

var newOrderedMap = yamldoc.NewOrderedMap

// document is a parsed description document: the shared reader, plus this
// format's own walk over it.
type document struct {
	*yamldoc.Document
}

func parseDocument(source []byte) (*document, error) {
	parsed, err := yamldoc.New(source)
	if err != nil {
		return nil, err
	}
	return &document{parsed}, nil
}

func isHTTPMethod(key string) bool { return yamldoc.IsHTTPMethod(key) }

func rawScalarWidth(n node) int { return yamldoc.ScalarWidth(n) }
