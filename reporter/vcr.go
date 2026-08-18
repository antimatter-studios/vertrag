package reporter

import (
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/antimatter-studios/vertrag/runner"
)

// VCR writes the run's traffic as a VCR cassette: the YAML file Ruby's VCR
// wrote first and vcrpy, Betamax and every other port copied. Its use is the
// opposite end of the same problem the HAR file solves — a HAR is for a person
// looking at what happened, a cassette is for a test suite standing in for the
// server, so a project can pin the API's real answers and run against them
// without the API being up.
//
// The shape is hand-written for the reason the JUnit schema is: nothing in Go
// writes these, only reads them, so a dependency would buy struct tags and cost
// a supply chain. It is also barely a format — a handful of keys with a
// reference implementation and no schema — so the choice is which readers to
// satisfy, and these keys are the intersection of the ones VCR and vcrpy both
// understand.
type VCR struct {
	Out io.Writer
	// Run says what produced the cassette. A recording committed to a
	// repository outlives every terminal that could have explained it.
	Run Provenance
	// Now stamps an exchange that carries no time of its own. It is a field so
	// a test can pin the bytes rather than assert around the clock.
	Now func() time.Time
}

type vcrCassette struct {
	HTTPInteractions []vcrInteraction `yaml:"http_interactions"`
	RecordedWith     string           `yaml:"recorded_with"`
	// RecordedFrom names the description, endpoint and config the recording
	// came from. It is vertrag's own key: every reader ignores what it does not
	// recognise, and a cassette that cannot say which endpoint it was taken
	// against is a set of answers with no question attached.
	RecordedFrom string `yaml:"recorded_from,omitempty"`
}

// vcrInteraction is one exchange. The three keys every reader looks for come
// first, in the order the reference implementation writes them; the two after
// are vertrag's own and sit last so that nothing a reader needs is buried under
// them. Schemathesis puts its own keys here too, which is precedent enough that
// no reader is going to be surprised by extras.
type vcrInteraction struct {
	Request    vcrRequest  `yaml:"request"`
	Response   vcrResponse `yaml:"response"`
	RecordedAt string      `yaml:"recorded_at"`
	Name       string      `yaml:"name,omitempty"`
	Status     string      `yaml:"status,omitempty"`
}

type vcrRequest struct {
	Method  string              `yaml:"method"`
	URI     string              `yaml:"uri"`
	Body    vcrBody             `yaml:"body"`
	Headers map[string][]string `yaml:"headers"`
}

type vcrResponse struct {
	Status  vcrStatus           `yaml:"status"`
	Headers map[string][]string `yaml:"headers"`
	Body    vcrBody             `yaml:"body"`
	// HTTPVersion is empty because the runner does not record which protocol
	// answered. The reference implementation leaves it empty too when its
	// adapter cannot supply one, so an empty value here is the format's own
	// idiom rather than a hole in this writer.
	HTTPVersion string `yaml:"http_version"`
}

type vcrStatus struct {
	Code    int    `yaml:"code"`
	Message string `yaml:"message"`
}

// vcrBody carries the payload and the name of the encoding it is in, which are
// Ruby's two words for a string: what the bytes are and how to read them.
type vcrBody struct {
	Encoding string `yaml:"encoding"`
	String   string `yaml:"string"`
}

// Report writes the cassette and returns true when the run passed.
func (r VCR) Report(results []runner.Result) bool {
	cassette := vcrCassette{
		HTTPInteractions: []vcrInteraction{},
		RecordedWith:     strings.TrimSpace("vertrag " + r.Run.Version),
		RecordedFrom:     r.Run.Source(),
	}

	recordedAt := clock(r.Now)
	for _, recorded := range exchanges(results, recordedAt) {
		cassette.HTTPInteractions = append(cassette.HTTPInteractions, vcrInteractionOf(recorded))
	}

	encoder := yaml.NewEncoder(r.Out)
	encoder.SetIndent(2)
	encoder.Encode(cassette)
	encoder.Close()

	return tally(results).Passed()
}

func vcrInteractionOf(recorded exchange) vcrInteraction {
	interaction := vcrInteraction{
		Request: vcrRequest{
			// Uppercase, as the wire spells it. The reference implementation
			// writes `get` because Ruby holds the method as a symbol, and a
			// reader matching a cassette against a live request compares
			// against the uppercase token the protocol defines.
			Method:  recorded.Method,
			URI:     recorded.URL,
			Body:    vcrBodyOf(recorded.RequestBody),
			Headers: vcrHeadersOf(recorded.RequestHeaders),
		},
		Response: vcrResponse{
			Status:  vcrStatus{Code: recorded.StatusCode, Message: recorded.StatusText},
			Headers: vcrHeadersOf(recorded.ResponseHeaders),
			Body:    vcrBodyOf(recorded.ResponseBody),
		},
		// HTTP-date, which is the format the reference implementation writes and
		// therefore the one its parsers accept.
		RecordedAt: recorded.Started.UTC().Format(http.TimeFormat),
		Name:       recorded.Name,
		Status:     string(recorded.Status),
	}

	if !recorded.Answered {
		// Code 0 for an exchange that got no reply, with the reason in the slot
		// the format keeps for a status message. It is the only place a
		// cassette can say this at all, and a replay library handed the
		// interaction answers 0 — which is the truth about what came back,
		// rather than a fabricated success the suite would then trust.
		interaction.Response.Status = vcrStatus{Code: 0, Message: recorded.Unanswered}
	}

	return interaction
}

// vcrHeadersOf renders headers as the format wants them: a name mapping to a
// list, because HTTP lets a header repeat.
//
// yaml.v3 writes a map's keys in sorted order, so the file is deterministic
// even though this is a map — worth knowing, because a hand-rolled writer put
// here in its place would lose that quietly and only a reader diffing two runs
// of the same suite would ever find out.
func vcrHeadersOf(pairs []nameValue) map[string][]string {
	headers := make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		headers[pair.Name] = append(headers[pair.Name], pair.Value)
	}
	return headers
}

// vcrBodyOf names the encoding honestly.
//
// A body that is not valid UTF-8 is written by yaml.v3 as a !!binary scalar,
// which round-trips the bytes exactly — so unlike the HAR file, a cassette
// keeps a binary payload intact. Calling it UTF-8 anyway would be a claim
// contradicted by the tag on the line below it; ASCII-8BIT is Ruby's name for
// bytes with no encoding, and a reader that acts on either word gets the right
// answer.
func vcrBodyOf(body string) vcrBody {
	if utf8.ValidString(body) {
		return vcrBody{Encoding: "UTF-8", String: body}
	}
	return vcrBody{Encoding: "ASCII-8BIT", String: body}
}
