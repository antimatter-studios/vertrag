package reporter

import (
	"encoding/json"
	"io"

	"github.com/antimatter-studios/vertrag/runner"
)

// JSON writes a run as one JSON document: what produced it, the counts, every
// transaction's verdict, and how much of the description the run reached.
//
// It is for a program rather than a person. JUnit is what a CI interface
// displays, and its schema has nowhere to put a figure except as a string in a
// property; a pipeline that wants to gate on, chart or diff a run reads this
// instead, without parsing XML or scraping a terminal log.
//
// The document carries verdicts, not traffic: what was sent and what came back
// in full is the job of the recordings, `har` and `vcr`. Each result keeps the
// request line and the status, which is what identifies it.
type JSON struct {
	Out io.Writer
	// Run says what produced the report, as it does in the JUnit file.
	Run Provenance
	// Surface is how much of the description the run reached. Nil leaves the
	// key out, which a caller building this reporter outside a run will want.
	Surface *Surface
	// Authorization is the authorization phase's matrix. Nil leaves the key
	// out, as it does for a run without the phase.
	Authorization *Authorization
}

type jsonDocument struct {
	Vertrag *jsonProvenance `json:"vertrag,omitempty"`
	Summary jsonSummary     `json:"summary"`
	Results []jsonResult    `json:"results"`
	Surface *Surface        `json:"surface,omitempty"`

	Authorization *jsonAuthorization `json:"authorization,omitempty"`
}

// jsonAuthorization is the matrix as the document carries it: normalised, so
// a reader sees the same cells and verdicts every other report shows.
type jsonAuthorization struct {
	Summary AuthorizationCounts `json:"summary"`
	Authorization
}

type jsonProvenance struct {
	Version  string `json:"version,omitempty"`
	Spec     string `json:"spec,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Config   string `json:"config,omitempty"`
}

type jsonSummary struct {
	Total   int  `json:"total"`
	Passing int  `json:"passing"`
	Failing int  `json:"failing"`
	Errors  int  `json:"errors"`
	Skipped int  `json:"skipped"`
	Passed  bool `json:"passed"`
	// DurationMS is a number of milliseconds rather than a Go duration
	// string, because "1m3.2s" is a format only Go reads.
	DurationMS int64 `json:"durationMs"`
}

type jsonResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Method string `json:"method,omitempty"`
	URI    string `json:"uri,omitempty"`
	// Expected and Actual are the statuses, which is what a program reading
	// verdicts compares; the bodies are in the recordings.
	Expected   string   `json:"expected,omitempty"`
	Actual     string   `json:"actual,omitempty"`
	DurationMS int64    `json:"durationMs"`
	Errors     []string `json:"errors,omitempty"`
	// Additional are the findings of checks Dredd does not make, kept apart
	// for the reason every other format keeps them apart.
	Additional []string `json:"additional,omitempty"`
}

// Report writes the document and returns true when the run passed.
func (r JSON) Report(results []runner.Result) bool {
	counted := tally(results)
	doc := jsonDocument{
		Summary: jsonSummary{
			Total: counted.Total, Passing: counted.Passing, Failing: counted.Failing,
			Errors: counted.Errors, Skipped: counted.Skipped, Passed: counted.Passed(),
			DurationMS: counted.Duration.Milliseconds(),
		},
		// An empty list rather than null, so a reader can iterate it without
		// first asking whether it is there.
		Results: []jsonResult{},
		Surface: r.Surface,
	}
	if r.Run != (Provenance{}) {
		doc.Vertrag = &jsonProvenance{Version: r.Run.Version, Spec: r.Run.Spec,
			Endpoint: r.Run.Endpoint, Config: r.Run.Config}
	}
	if r.Surface != nil && r.Surface.Required > 0 && !r.Surface.Met() {
		doc.Summary.Passed = false
	}
	if r.Authorization != nil {
		doc.Authorization = &jsonAuthorization{Summary: r.Authorization.Counts(), Authorization: r.Authorization.Normal()}
		if !r.Authorization.Met() {
			doc.Summary.Passed = false
		}
	}

	for _, result := range results {
		doc.Results = append(doc.Results, jsonResult{
			Name:       result.Name,
			Status:     string(result.Status),
			Method:     result.Request.Method,
			URI:        result.Request.URI,
			Expected:   result.Expected.StatusCode,
			Actual:     result.Actual.StatusCode,
			DurationMS: result.Duration.Milliseconds(),
			Errors:     result.Errors,
			Additional: result.Beyond,
		})
	}

	encoder := json.NewEncoder(r.Out)
	encoder.SetIndent("", "  ")
	// Transaction names are joined with " > ", which the default escaping
	// would write as > in every one of them.
	encoder.SetEscapeHTML(false)
	encoder.Encode(doc)

	return doc.Summary.Passed
}
