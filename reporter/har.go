package reporter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"
	"unicode/utf8"

	"github.com/antimatter-studios/vertrag/runner"
)

// HAR writes the run's traffic as an HTTP Archive — the file a browser's
// network panel exports, and the one every HTTP tool worth having can import.
// Dropping it into devtools, Insomnia, Postman or a HAR viewer gives the reader
// the run's requests as things they can inspect and resend, which is a shorter
// path from "CI says this failed" to "I have reproduced it" than any report.
//
// The schema is written out here rather than taken from a library, for the
// reason the JUnit schema is: the Go packages in this space parse HAR files
// rather than write them, so a dependency would buy these struct definitions
// and cost a supply chain.
//
// HAR 1.2 is a specification in the same sense JUnit XML is — one published
// draft, and then whatever the browsers did. The fields below are the ones the
// draft marks required plus the ones consumers actually read, and where a value
// genuinely is not known this says so with the draft's own -1 rather than
// guessing: a recording that claims it measured something it did not is worse
// than one that admits the gap.
type HAR struct {
	Out io.Writer
	// Run says what produced the recording. It becomes the log's creator and
	// its comment, so an archived file explains itself the way the JUnit
	// report's properties do.
	Run Provenance
	// Now stamps an exchange that carries no time of its own. It is a field so
	// a test can pin the bytes rather than assert around the clock.
	Now func() time.Time
}

// harTimeLayout is ISO 8601 with a timezone, which is what the draft asks for.
// Milliseconds are kept because a suite frequently sends several requests
// inside one second and a viewer orders its waterfall by this field.
const harTimeLayout = "2006-01-02T15:04:05.000Z07:00"

type harFile struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Creator harCreator `json:"creator"`
	Entries []harEntry `json:"entries"`
	Comment string     `json:"comment,omitempty"`
}

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type harEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            float64     `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
	Cache           harCache    `json:"cache"`
	Timings         harTimings  `json:"timings"`
	Comment         string      `json:"comment,omitempty"`
}

// harCache is the draft's required cache object. Nothing here consults a cache,
// so it is empty — and empty is what the draft says an entry with no cache
// information looks like, as against the field being missing.
type harCache struct{}

// harTimings breaks the request down into phases. Only the total is known: the
// runner measures a request from the call to the client until the body is read,
// and does not instrument the connection. -1 is the draft's value for a phase
// that was not measured, and it keeps the total honest, since the draft defines
// `time` as the sum of the phases that are not -1.
type harTimings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

type harRequest struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	HTTPVersion string       `json:"httpVersion"`
	Cookies     []harCookie  `json:"cookies"`
	Headers     []harHeader  `json:"headers"`
	QueryString []harHeader  `json:"queryString"`
	PostData    *harPostData `json:"postData,omitempty"`
	HeadersSize int          `json:"headersSize"`
	BodySize    int          `json:"bodySize"`
}

type harResponse struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []harCookie `json:"cookies"`
	Headers     []harHeader `json:"headers"`
	Content     harContent  `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int         `json:"headersSize"`
	BodySize    int         `json:"bodySize"`
	// Error is Chrome's convention for a request that got no response, and the
	// only place in the format that a failed exchange can explain itself. An
	// underscore marks it as an extension, which is how the draft says a
	// producer adds a field and a consumer knows to ignore one.
	Error string `json:"_error,omitempty"`
}

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// harCookie exists only to give the required cookies arrays a type. They are
// always empty, and deliberately so. Cookie and Set-Cookie are on the
// redaction list, so parsing either into this array would put the credential
// straight back into the file through a second door — the redaction would still
// be visible in the headers array and the reader would have no idea the value
// was also sitting three lines further down. The draft requires the field, so
// it is present and empty.
type harCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	// Encoding says the text is base64 rather than the payload. The draft gives
	// a response's content an encoding field and gives postData none, so this
	// is the extension form: without it a request body that is not valid UTF-8
	// reaches the file as U+FFFD replacement characters, because that is what
	// encoding/json does with bytes it cannot represent, and a fuzzed payload
	// recorded that way cannot be replayed or even identified.
	Encoding string `json:"_encoding,omitempty"`
}

type harContent struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

// Report writes the archive and returns true when the run passed.
func (r HAR) Report(results []runner.Result) bool {
	file := harFile{Log: harLog{
		Version: "1.2",
		Creator: harCreator{Name: "vertrag", Version: r.Run.Version},
		// The creator block already names the tool and its version, so the
		// comment carries only what it does not: which description, which
		// server, which config file.
		Comment: r.Run.Source(),
		// Never nil: a HAR with no entries is a valid recording of a run that
		// sent nothing, and a consumer reading null where the draft promises an
		// array fails at import with nothing useful to say about why.
		Entries: []harEntry{},
	}}

	for _, recorded := range exchanges(results, clock(r.Now)) {
		file.Log.Entries = append(file.Log.Entries, harEntryOf(recorded))
	}

	encoder := json.NewEncoder(r.Out)
	encoder.SetIndent("", "  ")
	// A recorded HTML or XML body is a wall of < escapes otherwise, which
	// is legal JSON that no consumer minds and no person can read. Half the
	// value of a recording is that a reader can see the payload in it.
	encoder.SetEscapeHTML(false)
	encoder.Encode(file)

	return tally(results).Passed()
}

func harEntryOf(recorded exchange) harEntry {
	elapsed := milliseconds(recorded.Duration)

	entry := harEntry{
		StartedDateTime: recorded.Started.Format(harTimeLayout),
		Time:            elapsed,
		Request:         harRequestOf(recorded),
		Response:        harResponseOf(recorded),
		Timings:         harTimings{Send: -1, Wait: elapsed, Receive: -1},
		// A viewer shows this beside the URL, which is what turns a wall of
		// identical-looking requests into the run the reader remembers: the
		// transaction's name and how it went.
		Comment: fmt.Sprintf("%s (%s)", recorded.Name, recorded.Status),
	}
	return entry
}

func harRequestOf(recorded exchange) harRequest {
	request := harRequest{
		Method: recorded.Method,
		URL:    recorded.URL,
		// The protocol version is not recorded. Writing HTTP/1.1 here would be
		// a claim about a connection nobody looked at, and against an endpoint
		// negotiating h2 it would be a false one. Nothing needs it to replay.
		HTTPVersion: "",
		Cookies:     []harCookie{},
		Headers:     harHeadersOf(recorded.RequestHeaders),
		QueryString: harHeadersOf(recorded.Query),
		// The raw header block was never held as bytes, so its size is not
		// something this can measure.
		HeadersSize: -1,
		BodySize:    len(recorded.RequestBody),
	}

	if recorded.RequestBody != "" {
		text, encoding := harPayload(recorded.RequestBody)
		request.PostData = &harPostData{
			MimeType: contentTypeOf(recorded.RequestHeaders),
			Text:     text,
			Encoding: encoding,
		}
	}
	return request
}

func harResponseOf(recorded exchange) harResponse {
	if !recorded.Answered {
		// Status 0 is what a browser writes for a request that got no answer,
		// and the reason the entry is here at all: a request that provoked a
		// timeout or a connection reset is the one a reader most wants to
		// resend, and dropping it would leave the recording claiming the run
		// never made it.
		return harResponse{
			Status:      0,
			HTTPVersion: "",
			Cookies:     []harCookie{},
			Headers:     []harHeader{},
			Content:     harContent{},
			BodySize:    -1,
			Error:       recorded.Unanswered,
		}
	}

	text, encoding := harPayload(recorded.ResponseBody)
	return harResponse{
		Status: recorded.StatusCode,
		// The reason phrase the server sent is not kept, so this is the
		// registered phrase for the code. They agree for every server that has
		// not customised its own, and an unregistered code leaves it empty
		// rather than inventing words for it.
		StatusText:  recorded.StatusText,
		HTTPVersion: "",
		Cookies:     []harCookie{},
		Headers:     harHeadersOf(recorded.ResponseHeaders),
		Content: harContent{
			Size:     len(recorded.ResponseBody),
			MimeType: contentTypeOf(recorded.ResponseHeaders),
			Text:     text,
			Encoding: encoding,
		},
		// Redirects are followed by the client before the runner sees a
		// response, so what is recorded here is always the end of the chain.
		RedirectURL: "",
		HeadersSize: -1,
		BodySize:    len(recorded.ResponseBody),
	}
}

// harPayload returns the text to record and the encoding that describes it.
//
// A body that is valid UTF-8 goes in as itself. One that is not is base64,
// because JSON cannot hold the bytes: encoding/json substitutes U+FFFD for
// every one it cannot represent, so a recorded binary download or a fuzzed
// payload would come back as a run of replacement characters — unreplayable,
// and with nothing in the file admitting the substitution happened.
func harPayload(body string) (text, encoding string) {
	if utf8.ValidString(body) {
		return body, ""
	}
	return base64.StdEncoding.EncodeToString([]byte(body)), "base64"
}

func harHeadersOf(pairs []nameValue) []harHeader {
	headers := make([]harHeader, 0, len(pairs))
	for _, pair := range pairs {
		headers = append(headers, harHeader{Name: pair.Name, Value: pair.Value})
	}
	return headers
}

// milliseconds is the unit every duration in a HAR is in, rounded so that the
// file is diffable and a viewer is not asked to render nanosecond precision it
// will throw away.
func milliseconds(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Microsecond)) / 1000
}
