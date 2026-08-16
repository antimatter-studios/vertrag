package corpus

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// Server answers the requests a description documents.
//
// It is built from the same document the tester is given, so adding a
// description to the corpus hosts it with no handler written to match — and,
// more usefully, the server cannot drift from the document the way a
// hand-written one does. A handler and a description are two statements of the
// same thing, and keeping two statements in agreement by hand is the problem
// contract testing exists to solve; solving it again here by hand would be
// perverse.
type Server struct {
	routes []route
	faults faultSet

	// stateful makes the server mint identifiers rather than echo the
	// documented one, and refuse any it did not mint.
	//
	// Without it a sequencing test proves nothing: the description's example
	// identifier would be accepted whether or not the link was followed, so the
	// run passes either way and the feature is untested. With it, a run in
	// document order asks for a widget nothing has created and gets a 404 —
	// which is the failure links exist to remove, and therefore the only way to
	// show they remove it.
	stateful bool
	minted   map[string]bool
	nextID   int
}

// Stateful returns a copy of the server that mints identifiers.
func (s *Server) Stateful() *Server {
	copied := *s
	copied.stateful = true
	copied.minted = map[string]bool{}
	copied.nextID = 41
	return &copied
}

// route is one documented exchange, with the template it answers on.
type route struct {
	method   string
	segments []segment
	response compile.Response
	// request is kept for its parameter and body schemas, which are what let a
	// conforming server refuse the input its description forbids.
	request compile.Request
}

// segment is one path component of a template, either a literal or a named
// variable. The name is kept rather than the position, because a template with
// two variables makes position ambiguous and the description has been telling
// us the name all along.
type segment struct {
	literal  string
	name     string
	variable bool
}

// New builds a server from a description document.
func New(description []byte, faults ...Fault) (*Server, error) {
	parsed, err := apidesc.Parse(description, "corpus")
	if err != nil {
		return nil, fmt.Errorf("parsing the description: %w", err)
	}

	result := compile.Compile(parsed.MediaType, parsed.Elements, "corpus")
	for _, annotation := range result.Annotations {
		if annotation.Type == "error" {
			return nil, fmt.Errorf("the description could not be read: %s", annotation.Message)
		}
	}

	server := &Server{faults: faultSet{}}
	for _, fault := range faults {
		server.faults[fault] = true
	}

	for _, transaction := range result.Transactions {
		server.routes = append(server.routes, route{
			method:   strings.ToUpper(transaction.Request.Method),
			segments: pathSegments(transaction.Request.Template, transaction.Request.URI),
			response: transaction.Response,
			request:  transaction.Request,
		})
	}
	return server, nil
}

// NewNamed builds a server for a corpus description.
func NewNamed(name string, faults ...Fault) (*Server, error) {
	source, err := Load(name)
	if err != nil {
		return nil, err
	}
	return New(source, faults...)
}

// pathSegments reads the path a template matches on.
//
// The query portion is dropped: a documented query parameter does not decide
// which operation a request is for, and requiring it to match would make a
// request omitting an optional parameter reach nothing at all.
func pathSegments(template, uri string) []segment {
	source := template
	if source == "" {
		// A description with no template still has the expanded URI, which is
		// the same path for any operation without path parameters.
		source = uri
	}
	// Everything from the first query expression onwards describes the query
	// string rather than the path.
	if cut := strings.IndexAny(source, "?"); cut >= 0 {
		source = source[:cut]
	}
	source = strings.TrimSuffix(strings.TrimSuffix(source, "{"), "{&")

	var segments []segment
	for _, part := range strings.Split(strings.Trim(source, "/"), "/") {
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "{") {
			name := strings.Trim(part, "{}")
			// A template variable may carry an operator or a modifier — {+id},
			// {id*}, {id:3} — none of which is part of the name.
			name = strings.TrimLeft(name, "+#./;?&")
			if cut := strings.IndexAny(name, "*:"); cut >= 0 {
				name = name[:cut]
			}
			segments = append(segments, segment{variable: true, name: name})
			continue
		}
		segments = append(segments, segment{literal: part})
	}
	return segments
}

// Handler serves the description.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matched, ok := s.match(r)
		if !ok {
			writeJSON(w, http.StatusNotFound, `{"error":"no such operation"}`, nil)
			return
		}
		s.answer(w, r, matched)
	})
}

// match finds the route a request is for.
func (s *Server) match(r *http.Request) (route, bool) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		parts = nil
	}

	for _, candidate := range s.routes {
		if candidate.method != r.Method || len(candidate.segments) != len(parts) {
			continue
		}
		fits := true
		for i, seg := range candidate.segments {
			if !seg.variable && seg.literal != parts[i] {
				fits = false
				break
			}
		}
		if fits {
			return candidate, true
		}
	}
	return route{}, false
}

// answer replies as the description promises, deviating only where a fault says
// to.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, matched route) {
	// Input the description forbids is refused, which is what makes the
	// generated-input tests meaningful: a server that accepted everything
	// would report a validation bypass on every drawn value and prove nothing.
	if reason, bad := s.rejects(r, matched); bad {
		if s.faults.has(FaultCrashesOnBadInput) {
			writeJSON(w, http.StatusInternalServerError, `{"error":"crashed on input it should have refused"}`, nil)
			return
		}
		writeJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"error":%q}`, reason), nil)
		return
	}

	if s.faults.has(FaultRejectsValidInput) {
		writeJSON(w, http.StatusBadRequest, `{"error":"refusing input the description permits"}`, nil)
		return
	}

	status, err := strconv.Atoi(strings.TrimSpace(matched.response.Status))
	if err != nil {
		status = http.StatusOK
	}

	if s.stateful {
		if reply, handled := s.statefulAnswer(w, r, matched, status); handled {
			_ = reply
			return
		}
	}
	if s.faults.has(FaultWrongStatus) {
		status = http.StatusInternalServerError
	}

	headers := s.headersFor(matched)

	body := matched.response.Body
	if s.faults.has(FaultBodyViolatesSchema) {
		body = violate(body)
	}
	if s.faults.has(FaultMissingProperty) {
		body = dropAProperty(body)
	}

	contentType := headerValue(headers, "Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if s.faults.has(FaultWrongContentType) {
		// Still valid JSON, so only a check that compares the media type
		// itself can see this.
		contentType = "text/plain"
	}
	headers["Content-Type"] = contentType

	writeJSON(w, status, body, headers)
}

// headersFor builds the response headers the description declares.
func (s *Server) headersFor(matched route) map[string]string {
	headers := map[string]string{}
	for _, header := range matched.response.Headers {
		if strings.EqualFold(header.Name, "Content-Type") {
			headers[header.Name] = header.Value
			continue
		}
		if s.faults.has(FaultMissingHeader) {
			continue
		}
		value := header.Value
		if schema, declared := schemaFor(matched.response.HeaderSchemas, header.Name); declared {
			// A declared header usually has a schema and no example, so there
			// is no documented value to echo and one has to be invented that
			// the schema permits.
			value = headerValueFor(schema)
			if s.faults.has(FaultHeaderViolatesSchema) {
				value = "not-what-the-schema-says"
			}
		}
		headers[header.Name] = value
	}
	return headers
}

// schemaFor finds a header's schema without assuming how it was cased.
//
// The keys come from the description by way of the compiler, which preserves
// whatever the document wrote, while HTTP treats header names case
// insensitively. Matching on the exact string silently found nothing and the
// server sent an empty header, which the tester then correctly reported.
func schemaFor(schemas map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	for declared, schema := range schemas {
		if strings.EqualFold(declared, name) {
			return schema, true
		}
	}
	return nil, false
}

// headerValueFor invents a value satisfying a header's schema, since a
// description declares the shape of these rather than an example.
//
// It honours the constraints rather than picking by type alone. An earlier
// version returned "corpus" for every string, which a header declaring an enum
// forbids — so the reference server sent a value its own description called
// invalid, and the conformance test reported vertrag for catching it correctly.
func headerValueFor(schema json.RawMessage) string {
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return "1"
	}

	// An enum lists every permitted value, so nothing outside it will do.
	if values, ok := decoded["enum"].([]any); ok && len(values) > 0 {
		return stringifyScalar(values[0])
	}
	if fixed, ok := decoded["const"]; ok {
		return stringifyScalar(fixed)
	}

	constraints := generate.Constraints{
		Pattern: text(decoded["pattern"]),
		Format:  text(decoded["format"]),

		MinLength: number(decoded["minLength"]),
		MaxLength: number(decoded["maxLength"]),
		Minimum:   number(decoded["minimum"]),
		Maximum:   number(decoded["maximum"]),
	}

	switch decoded["type"] {
	case "integer", "number":
		// Zero unless a bound moves it, matching how a specimen is chosen
		// everywhere else.
		return strconv.FormatFloat(constraints.Number(0), 'f', -1, 64)
	case "boolean":
		return "true"
	default:
		if value := constraints.String(); value != "" {
			return value
		}
		return "corpus"
	}
}

func text(value any) string {
	out, _ := value.(string)
	return out
}

func number(value any) *float64 {
	out, ok := value.(float64)
	if !ok {
		return nil
	}
	return &out
}

func stringifyScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(value)
	}
}

// rejects reports whether a conforming server would refuse this request.
//
// The check is the description's own schemas, applied through the same
// validator the tester uses. That is deliberate rather than lazy: a reference
// server enforcing something subtly different from what its document says would
// make every disagreement ambiguous, and the disagreement is the thing being
// measured.
func (s *Server) rejects(r *http.Request, matched route) (string, bool) {
	if !s.faults.has(FaultAcceptsAnyParameter) {
		if reason, bad := s.rejectsParameters(r, matched); bad {
			return reason, true
		}
	}
	if !s.faults.has(FaultAcceptsAnyBody) {
		if reason, bad := rejectsBody(r, matched); bad {
			return reason, true
		}
	}
	return "", false
}

func (s *Server) rejectsParameters(r *http.Request, matched route) (string, bool) {
	pathValues := pathValuesOf(matched.segments, r.URL.Path)

	for _, parameter := range matched.request.Parameters {
		if strings.TrimSpace(parameter.Schema) == "" {
			continue
		}

		var raw string
		var present bool
		switch parameter.In {
		case compile.InPath:
			raw, present = pathValues[parameter.Name], true
		case compile.InQuery:
			raw, present = r.URL.Query().Get(parameter.Name), r.URL.Query().Has(parameter.Name)
		case compile.InHeader:
			raw = r.Header.Get(parameter.Name)
			present = raw != ""
		}
		if !present {
			continue
		}

		problems := validate.AgainstHeaderSchemas(
			map[string]json.RawMessage{strings.ToLower(parameter.Name): json.RawMessage(parameter.Schema)},
			map[string]string{parameter.Name: raw},
		)
		if len(problems) > 0 {
			return fmt.Sprintf("%s parameter %q is not permitted: %s",
				parameter.In, parameter.Name, strings.Join(problems, "; ")), true
		}
	}
	return "", false
}

func rejectsBody(r *http.Request, matched route) (string, bool) {
	if strings.TrimSpace(matched.request.Schema) == "" || r.Body == nil {
		return "", false
	}

	body := readAll(r)
	if strings.TrimSpace(body) == "" {
		return "", false
	}

	if result := validate.AgainstSchema(json.RawMessage(matched.request.Schema), body); !result.Valid {
		return "the request body is not permitted: " + strings.Join(result.Errors, "; "), true
	}
	return "", false
}

// pathValuesOf reads the values a request supplied for a template's variables.
func pathValuesOf(segments []segment, requestPath string) map[string]string {
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	values := map[string]string{}
	for i, seg := range segments {
		if seg.variable && i < len(parts) {
			values[seg.name] = parts[i]
		}
	}
	return values
}

func readAll(r *http.Request) string {
	var b strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buffer)
		b.Write(buffer[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, body string, headers map[string]string) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

// violate rewrites a documented body so its values break the schema's
// constraints while its shape stays intact.
//
// The shape has to survive, because a body of the wrong shape is caught by any
// check that looks at property names — which is what Dredd does without a
// schema. This fault exists to distinguish a tester that reads the constraints
// from one that only counts properties, so breaking the shape would defeat it.
func violate(body string) string {
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return body
	}

	rewritten, err := json.Marshal(exceedBounds(decoded))
	if err != nil {
		return body
	}
	return string(rewritten)
}

// exceedBounds walks a value, pushing every scalar past the bounds a corpus
// description states: strings become far longer than any maxLength here, and
// numbers go negative where every minimum is at least zero.
func exceedBounds(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			typed[key] = exceedBounds(nested)
		}
		return typed
	case []any:
		for i, nested := range typed {
			typed[i] = exceedBounds(nested)
		}
		return typed
	case string:
		return strings.Repeat("x", 200)
	case float64:
		return -1
	default:
		return value
	}
}

// dropAProperty removes one property a schema requires.
//
// The first by sorted name rather than an arbitrary one, so the same fault
// produces the same body on every run and a failing test can be read twice.
func dropAProperty(body string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || len(decoded) == 0 {
		return body
	}

	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	sort.Strings(names)
	delete(decoded, names[0])

	rewritten, err := json.Marshal(decoded)
	if err != nil {
		return body
	}
	return string(rewritten)
}

// statefulAnswer mints identifiers on creation and refuses unknown ones.
//
// Deliberately narrow: it acts only on a 2xx that carries an `id`, and on a
// path whose last segment is a variable. A corpus server that tried to model a
// real datastore would become a thing needing its own tests.
func (s *Server) statefulAnswer(w http.ResponseWriter, r *http.Request, matched route, status int) (string, bool) {
	last := ""
	if n := len(matched.segments); n > 0 && matched.segments[n-1].variable {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		last = parts[len(parts)-1]
	}

	// Reading something never created is a 404, which is precisely what a run
	// in document order does before the create has happened.
	if last != "" && !s.minted[last] {
		writeJSON(w, http.StatusNotFound, `{"error":"no such widget"}`, nil)
		return "", true
	}

	if status < 200 || status > 299 || last != "" {
		return "", false
	}

	var body map[string]any
	if json.Unmarshal([]byte(matched.response.Body), &body) != nil {
		return "", false
	}
	if _, carries := body["id"]; !carries {
		return "", false
	}

	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.minted[id] = true
	body["id"] = s.nextID

	rewritten, err := json.Marshal(body)
	if err != nil {
		return "", false
	}
	writeJSON(w, status, string(rewritten), s.headersFor(matched))
	return string(rewritten), true
}
