package reporter

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// recordedAt is the instant every cassette test stamps, so the assertions can be
// about the bytes written rather than about whatever second the suite ran in.
var recordedAt = time.Date(2026, time.August, 18, 9, 30, 15, 500*int(time.Millisecond), time.UTC)

func fixedClock() func() time.Time { return func() time.Time { return recordedAt } }

// oneExchange is a transaction with a credential on the way out and a
// Set-Cookie on the way back, which is the shape that makes the redaction
// assertions worth making.
func oneExchange() runner.Result {
	return runner.Result{
		Name:   "GET /things > 200",
		Status: runner.StatusPass,
		Request: runner.Request{
			Method: "GET",
			URI:    "/things?limit=2",
			URL:    "http://localhost:4000/things?limit=2",
			Headers: map[string]string{
				"Authorization": "Bearer sk-live-supersecret",
				"Content-Type":  "application/json",
				"X-Api-Key":     "ak_9999",
			},
			Body: `{"sent":true}`,
		},
		Actual: validate.Message{
			StatusCode: "200",
			Headers: map[string]string{
				"Content-Type": "application/json",
				"Set-Cookie":   "session=abcdef; HttpOnly",
			},
			Body: `{"things":[]}`,
		},
		Started:  recordedAt,
		Duration: 12*time.Millisecond + 340*time.Microsecond,
	}
}

func harOf(t *testing.T, results []runner.Result) harFile {
	t.Helper()
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report(results)

	var parsed harFile
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the archive does not parse as JSON: %v\n%s", err, out.String())
	}
	return parsed
}

func cassetteOf(t *testing.T, results []runner.Result) vcrCassette {
	t.Helper()
	var out bytes.Buffer
	VCR{Out: &out, Now: fixedClock()}.Report(results)

	var parsed vcrCassette
	if err := yaml.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the cassette does not parse as YAML: %v\n%s", err, out.String())
	}
	return parsed
}

// TestBothCassetteFormatsRecordTheSameExchanges pins the property that makes it
// safe to ask for both files from one run. Two recordings of the same traffic
// that disagree about how much traffic there was is worse than either of them
// being wrong: the reader has no way to tell which file lied.
func TestBothCassetteFormatsRecordTheSameExchanges(t *testing.T) {
	results := []runner.Result{
		oneExchange(),
		{
			Name: "POST /things > 201", Status: runner.StatusFail,
			Request: runner.Request{Method: "POST", URL: "http://localhost:4000/things"},
			Actual:  validate.Message{StatusCode: "500"},
			Errors:  []string{"statusCode: expected 201, got 500"},
		},
		{
			Name: "DELETE /things/1 > 204", Status: runner.StatusError,
			Request: runner.Request{Method: "DELETE", URL: "http://localhost:4000/things/1"},
			Errors:  []string{"dial tcp: connection refused"},
		},
		{
			Name: "GET /skipped > 200", Status: runner.StatusSkip,
			Request: runner.Request{Method: "GET", URL: "http://localhost:4000/skipped"},
		},
	}

	archive := harOf(t, results)
	cassette := cassetteOf(t, results)

	if len(archive.Log.Entries) != len(cassette.HTTPInteractions) {
		t.Fatalf("har recorded %d entries and the cassette %d",
			len(archive.Log.Entries), len(cassette.HTTPInteractions))
	}
	for i := range archive.Log.Entries {
		gotHAR := archive.Log.Entries[i].Request.URL
		gotVCR := cassette.HTTPInteractions[i].Request.URI
		if gotHAR != gotVCR {
			t.Errorf("entry %d: har recorded %q, the cassette %q", i, gotHAR, gotVCR)
		}
	}
}

// TestACassetteOmitsATransactionThatWasNeverSent pins that a skip produces no
// entry. A recording is of traffic, and an entry for a transaction a hook
// removed would be a request nobody made — replayed later, it would teach a
// suite to expect a call that never happens.
func TestACassetteOmitsATransactionThatWasNeverSent(t *testing.T) {
	results := []runner.Result{
		oneExchange(),
		{
			Name: "GET /skipped > 200", Status: runner.StatusSkip,
			Request: runner.Request{Method: "GET", URL: "http://localhost:4000/skipped"},
			Errors:  []string{"skipped by a hook"},
		},
	}

	archive := harOf(t, results)
	if len(archive.Log.Entries) != 1 {
		t.Fatalf("entries = %d, want only the transaction that was sent", len(archive.Log.Entries))
	}
	if got := archive.Log.Entries[0].Request.URL; got != "http://localhost:4000/things?limit=2" {
		t.Errorf("the recorded entry is %q, want the one that was sent", got)
	}

	cassette := cassetteOf(t, results)
	if len(cassette.HTTPInteractions) != 1 {
		t.Fatalf("interactions = %d, want only the transaction that was sent",
			len(cassette.HTTPInteractions))
	}
}

// TestACassetteRecordsTheAbsoluteURL pins the bug this feature would otherwise
// have shipped with. Request.URI is relative and only means anything beside the
// endpoint the run was aimed at, which a recording does not otherwise repeat —
// a cassette of `/things` cannot be replayed against anything.
func TestACassetteRecordsTheAbsoluteURL(t *testing.T) {
	results := []runner.Result{oneExchange()}

	if got := harOf(t, results).Log.Entries[0].Request.URL; got != "http://localhost:4000/things?limit=2" {
		t.Errorf("har url = %q, want the absolute address", got)
	}
	if got := cassetteOf(t, results).HTTPInteractions[0].Request.URI; got != "http://localhost:4000/things?limit=2" {
		t.Errorf("cassette uri = %q, want the absolute address", got)
	}
}

// TestACassetteStampsAnExchangeThatCarriesNoTimeOfItsOwn pins the fallback. The
// probing commands assemble their results by hand and record no start time, and
// both formats require one per entry — a zero time would be recorded as the year
// 1, which a viewer either refuses or draws a two-thousand-year waterfall for.
func TestACassetteStampsAnExchangeThatCarriesNoTimeOfItsOwn(t *testing.T) {
	probed := oneExchange()
	probed.Started = time.Time{}

	got := harOf(t, []runner.Result{probed}).Log.Entries[0].StartedDateTime
	if want := "2026-08-18T09:30:15.500Z"; got != want {
		t.Errorf("startedDateTime = %q, want the report's own clock %q", got, want)
	}

	recorded := cassetteOf(t, []runner.Result{probed}).HTTPInteractions[0].RecordedAt
	if want := "Tue, 18 Aug 2026 09:30:15 GMT"; recorded != want {
		t.Errorf("recorded_at = %q, want %q", recorded, want)
	}
}

// TestACassetteIsByteIdenticalForTheSameRun pins determinism. Header and query
// maps are walked in Go's random order unless something sorts them, and a
// pipeline diffing today's recording against yesterday's would see change in
// every entry where nothing changed at all.
func TestACassetteIsByteIdenticalForTheSameRun(t *testing.T) {
	results := []runner.Result{oneExchange()}

	for _, format := range []struct {
		name  string
		write func(out *bytes.Buffer)
	}{
		{"har", func(out *bytes.Buffer) { HAR{Out: out, Now: fixedClock()}.Report(results) }},
		{"vcr", func(out *bytes.Buffer) { VCR{Out: out, Now: fixedClock()}.Report(results) }},
	} {
		var first bytes.Buffer
		format.write(&first)
		for i := 0; i < 20; i++ {
			var again bytes.Buffer
			format.write(&again)
			if again.String() != first.String() {
				t.Fatalf("%s is not deterministic:\n%s\n---\n%s", format.name, first.String(), again.String())
			}
		}
	}
}

// TestACassetteReportsTheRunsVerdict pins that adding a recording to a
// pipeline's reporter list cannot change its exit status. Multi passes only if
// every reporter agrees the run passed, so a cassette that returned true for a
// failing run would turn a red build green.
func TestACassetteReportsTheRunsVerdict(t *testing.T) {
	failing := []runner.Result{{
		Name: "POST /things > 201", Status: runner.StatusFail,
		Request: runner.Request{Method: "POST", URL: "http://localhost:4000/things"},
		Actual:  validate.Message{StatusCode: "500"},
		Errors:  []string{"statusCode: expected 201, got 500"},
	}}

	var out bytes.Buffer
	har := HAR{Out: &out, Now: fixedClock()}
	vcr := VCR{Out: &out, Now: fixedClock()}

	if har.Report(failing) {
		t.Error("a har of a failing run should not report the run as passing")
	}
	out.Reset()
	if vcr.Report(failing) {
		t.Error("a cassette of a failing run should not report the run as passing")
	}

	passing := []runner.Result{oneExchange()}
	out.Reset()
	if !har.Report(passing) {
		t.Error("a har of a clean run should report the run as passing")
	}
	out.Reset()
	if !vcr.Report(passing) {
		t.Error("a cassette of a clean run should report the run as passing")
	}
}
