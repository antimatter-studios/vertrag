package hooks

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// Mirror a no-op worker's JSON round trip at every hook phase. Its wire shape
// always contains an empty body, so it must neither erase nor invent intent.
type emptyBodyRoundTrip struct{}

func (emptyBodyRoundTrip) BeforeAll(txs []*runner.Transaction) error {
	for _, tx := range txs {
		if err := (emptyBodyRoundTrip{}).BeforeEach(tx); err != nil {
			return err
		}
	}
	return nil
}
func (emptyBodyRoundTrip) AfterAll(txs []*runner.Transaction) error {
	return (emptyBodyRoundTrip{}).BeforeAll(txs)
}
func (emptyBodyRoundTrip) BeforeEach(tx *runner.Transaction) error {
	data, err := json.Marshal(toWire(tx))
	if err != nil {
		return err
	}
	var wire wireTransaction
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	applyWire(tx, wire)
	return nil
}
func (emptyBodyRoundTrip) BeforeEachValidation(tx *runner.Transaction) error {
	return (emptyBodyRoundTrip{}).BeforeEach(tx)
}
func (emptyBodyRoundTrip) AfterEach(tx *runner.Transaction) error {
	return (emptyBodyRoundTrip{}).BeforeEach(tx)
}

func TestEmptyBodyIntentSurvivesHookWireAndHTTP(t *testing.T) {
	for _, content := range []string{`{"example":""}`, `{}`} {
		for _, actual := range []string{"", "unexpected"} {
			t.Run(content+"/"+actual, func(t *testing.T) {
				data := []byte(`{"openapi":"3.1.0","info":{"title":"empty body","version":"1"},"paths":{"/":{"get":{"responses":{"200":{"description":"OK","content":{"text/plain":` + content + `}}}}}}}`)
				parsed, err := openapi3.Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				txs := compile.Compile("application/vnd.oai.openapi", parsed, "empty.json").Transactions
				if len(txs) != 1 {
					t.Fatalf("got %d transactions", len(txs))
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					_, _ = io.WriteString(w, actual)
				}))
				defer server.Close()
				engine := runner.New(server.URL)
				engine.Hooks = emptyBodyRoundTrip{}
				results, err := engine.Run(t.Context(), txs)
				if err != nil || len(results) != 1 {
					t.Fatalf("results %v, error %v", results, err)
				}
				want := runner.StatusPass
				if content != `{}` && actual != "" {
					want = runner.StatusFail
				}
				if results[0].Status != want {
					t.Fatalf("status %s, want %s: %v", results[0].Status, want, results[0].Errors)
				}
			})
		}
	}
}
