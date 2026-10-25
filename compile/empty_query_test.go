package compile

import (
	"testing"

	"github.com/antimatter-studios/vertrag/refract"
)

func TestEmptyURIExamplesRespectTheirPositionAndPresence(t *testing.T) {
	for _, test := range []struct {
		name, template, want string
		present              bool
	}{
		{"query", "/lookup{?value}", "/lookup?value=", true},
		{"continuation query", "/lookup?fixed=1{&value}", "/lookup?fixed=1&value=", true},
		{"exploded query", "/lookup{?value*}", "/lookup?value=", true},
		{"missing query", "/lookup{?value}", "", false},
		{"empty path", "/lookup/{value}", "", true},
		{"missing path", "/lookup/{value}", "", false},
		{"empty path segment", "/lookup{/value}", "", true},
		{"shared path and query", "/lookup/{value}{?value}", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := refract.New("string")
			if test.present {
				value = refract.String("")
			}
			member := refract.Member("value", value)
			member.SetAttr("typeAttributes", refract.Array(refract.String("required")))
			resource := resource(test.template, "", transition("Lookup", transaction("200")))
			resource.SetAttr("hrefVariables", refract.Named("hrefVariables", member))
			result := Compile("application/vnd.oai.openapi", buildAPI(resource), "")
			if test.want == "" {
				if len(result.Transactions) != 0 || len(result.Annotations) == 0 {
					t.Fatalf("unusable required parameter accepted: %+v", result)
				}
				return
			}
			if len(result.Transactions) != 1 {
				t.Fatalf("expected one transaction: %+v", result)
			}
			if got := result.Transactions[0].Request.URI; got != test.want {
				t.Fatalf("URI %q, want %q", got, test.want)
			}
			for _, a := range result.Annotations {
				if a.Type == "error" {
					t.Fatalf("unexpected error: %+v", a)
				}
			}
		})
	}
}
