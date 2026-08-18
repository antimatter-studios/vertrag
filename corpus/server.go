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

	// answered counts how many times each method-and-path has been asked, so
	// an operation documenting several outcomes can give each in turn.
	//
	// A description saying one request yields both a 200 and a 404 is
	// describing two states of the world, and a tester asks for each in
	// document order. No stateless server can satisfy that from the request
	// alone — the requests are identical — so answering them in the order they
	// are asked is the only way to satisfy the document at all. It is also
	// exactly the awkwardness real projects hit, and solve with a hook that
	// skips one.
	answered map[string]int

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
	// deleted remembers what was removed, so the lingering-resource fault has
	// something to linger: a server that never held the id at all should 404
	// whatever its faults.
	deleted map[string]bool
	nextID  int
}

// Stateful returns a copy of the server that mints identifiers.
func (s *Server) Stateful() *Server {
	copied := *s
	copied.answered = map[string]int{}
	copied.stateful = true
	copied.minted = map[string]bool{}
	copied.deleted = map[string]bool{}
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

	server := &Server{faults: faultSet{}, answered: map[string]int{}}
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

	var matching []route
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
			matching = append(matching, candidate)
		}
	}

	if len(matching) == 0 {
		return route{}, false
	}

	// Several documented outcomes for one request: give each in turn, in
	// document order, which is the order they are asked for.
	key := r.Method + " " + r.URL.Path
	at := s.answered[key]
	s.answered[key] = at + 1
	if at >= len(matching) {
		at = len(matching) - 1
	}
	return matching[at], true
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

	// A documented range is answered with the lowest code in its band: `2XX`
	// gets a 200, `4XX` a 400. Parsing the text as a number gave every range a
	// 200, so a description promising `4XX` was served a success and the
	// conformance baseline measured the wrong thing entirely.
	status := validate.StatusBandBase(matched.response.Status)
	if status == 0 {
		status = http.StatusOK
	}

	if s.faults.has(FaultWrongStatus) {
		status = http.StatusInternalServerError
	}

	// After the faults, not before. Minting an identifier ran first and
	// answered outright, so a stateful server ignored every fault on its
	// creating operations — the fault was silently not committed, and a
	// sequencing test measuring "what happens when the first step fails" was
	// measuring a first step that passed.
	if s.stateful {
		if handled := s.statefulAnswer(w, r, matched, status); handled {
			return
		}
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
		case compile.InCookie:
			// Read back off the wire rather than out of the header text, so
			// the server judges the cookie a standard parser would see — which
			// is the only cookie a real handler could act on.
			if cookie, err := r.Cookie(parameter.Name); err == nil {
				raw, present = cookie.Value, true
			}
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
	// A multipart body is checked by parsing it, because that is the only
	// question worth asking of one: a standard parser either reads the parts or
	// it does not, and a body that only vertrag can read is a body no server
	// can. Its schema describes the parts rather than a JSON document, so
	// validating the raw text against it would be meaningless.
	if isMultipart(r) {
		return rejectsMultipart(r, matched)
	}
	// A form-encoded body is what an HTML form posts. A real server parses
	// the fields and reads each to the type its schema declares, and that is
	// what is judged — never the raw `a=1&b=x` text against a JSON Schema,
	// which would refuse every form ever sent.
	if isFormEncoded(r) {
		return rejectsForm(r, matched)
	}

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
func (s *Server) statefulAnswer(w http.ResponseWriter, r *http.Request, matched route, status int) bool {
	last := ""
	if n := len(matched.segments); n > 0 && matched.segments[n-1].variable {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		last = parts[len(parts)-1]
	}

	// Reading something never created is a 404, which is precisely what a run
	// in document order does before the create has happened.
	//
	// Unless the server is pretending a deleted resource is still there: that
	// fault answers as though it existed, which is what a use-after-free
	// looks like from outside.
	if last != "" && !s.minted[last] && !(s.deleted[last] && s.faults.has(FaultResourceLingersAfterDelete)) {
		writeJSON(w, http.StatusNotFound, `{"error":"no such widget"}`, nil)
		return true
	}

	// A successful DELETE unmints: the resource is gone, and a later read of
	// it must not find it. Without this the corpus could not tell a server
	// that really deletes from one that only says it does.
	if last != "" && strings.EqualFold(r.Method, http.MethodDelete) && status >= 200 && status <= 299 {
		delete(s.minted, last)
		s.deleted[last] = true
		return false
	}

	// Only a successful creation mints. A faulted status is not a creation,
	// so the fault reaches the client and nothing is recorded — which is what
	// makes a failed first step genuinely fail.
	if status < 200 || status > 299 || last != "" {
		return false
	}

	var body map[string]any
	if json.Unmarshal([]byte(matched.response.Body), &body) != nil {
		return false
	}
	if _, carries := body["id"]; !carries {
		return false
	}

	s.nextID++
	id := strconv.Itoa(s.nextID)
	// The fault that claims a creation and stores nothing: the response is
	// the same, so the create passes; only following the link to read it
	// back shows the two disagree.
	if !s.faults.has(FaultCreatedResourceMissing) {
		s.minted[id] = true
	}
	body["id"] = s.nextID

	rewritten, err := json.Marshal(body)
	if err != nil {
		return false
	}
	writeJSON(w, status, string(rewritten), s.headersFor(matched))
	return true
}

// isMultipart reports whether the request carries a multipart body.
func isMultipart(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/")
}

// rejectsMultipart reads the parts with the standard library and checks that
// every property the schema requires arrived as one.
//
// This is what makes the multipart corpus worth having. vertrag assembles these
// from the schema where Dredd sends an empty body, and an assembly nobody but
// vertrag can parse would look identical from the outside — the request goes
// out, the server answers, the test passes, and no upload endpoint is actually
// being exercised.
func rejectsMultipart(r *http.Request, matched route) (string, bool) {
	if err := r.ParseMultipartForm(readLimit); err != nil {
		return "the multipart body could not be parsed: " + err.Error(), true
	}

	var schema map[string]any
	if json.Unmarshal([]byte(matched.request.Schema), &schema) != nil {
		return "", false
	}
	required, _ := schema["required"].([]any)

	for _, name := range required {
		field, ok := name.(string)
		if !ok {
			continue
		}
		if _, present := r.MultipartForm.Value[field]; present {
			continue
		}
		if _, present := r.MultipartForm.File[field]; present {
			continue
		}
		return "the multipart body carries no part named " + field, true
	}

	// The non-file parts are fields like any form's, and a real server
	// reads each to its declared type and checks it: a `size` part that
	// says "abc" or -1 is refused. File parts stand for their presence,
	// which the loop above has already established.
	properties, _ := schema["properties"].(map[string]any)
	object := make(map[string]any, len(r.MultipartForm.Value)+len(r.MultipartForm.File))
	for name, values := range r.MultipartForm.Value {
		if len(values) == 0 {
			continue
		}
		property, _ := properties[name].(map[string]any)
		object[name] = readFormField(property, values[0])
	}
	for name := range r.MultipartForm.File {
		// A file part satisfies its schema by arriving; a placeholder that
		// is a string keeps the object valid against {type: string, format:
		// binary} without pretending to be the bytes.
		object[name] = "vertrag placeholder"
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", false
	}
	if result := validate.AgainstSchema(json.RawMessage(matched.request.Schema), string(encoded)); !result.Valid {
		return "the multipart body is not permitted: " + strings.Join(result.Errors, "; "), true
	}
	return "", false
}

// isFormEncoded reports whether the request declares a form-encoded body.
func isFormEncoded(r *http.Request) bool {
	media := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	return media == "application/x-www-form-urlencoded"
}

// rejectsForm parses a form-encoded body the way a real server does — each
// field read to the type its property schema declares — and validates the
// resulting object against the schema. A field that does not parse as its
// declared type stays text, which the schema then rejects: exactly the
// behaviour of a handler that validated its input.
func rejectsForm(r *http.Request, matched route) (string, bool) {
	if strings.TrimSpace(matched.request.Schema) == "" {
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		return "the form body could not be parsed: " + err.Error(), true
	}

	var schema map[string]any
	if json.Unmarshal([]byte(matched.request.Schema), &schema) != nil {
		return "", false
	}
	properties, _ := schema["properties"].(map[string]any)

	object := make(map[string]any, len(r.PostForm))
	for name := range r.PostForm {
		text := r.PostForm.Get(name)
		property, _ := properties[name].(map[string]any)
		object[name] = readFormField(property, text)
	}

	encoded, err := json.Marshal(object)
	if err != nil {
		return "", false
	}
	if result := validate.AgainstSchema(json.RawMessage(matched.request.Schema), string(encoded)); !result.Valid {
		return "the form body is not permitted: " + strings.Join(result.Errors, "; "), true
	}
	return "", false
}

// readFormField parses one field's text to the type its schema declares, or
// leaves it as text when it does not parse — which is what the schema will
// then reject.
func readFormField(property map[string]any, text string) any {
	declared, _ := property["type"].(string)
	switch declared {
	case "integer", "number":
		if n, err := strconv.ParseFloat(text, 64); err == nil {
			return n
		}
	case "boolean":
		switch text {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return text
}

// readLimit bounds what ParseMultipartForm keeps in memory. Generous for a
// corpus body, which is a placeholder rather than a real upload.
const readLimit = 1 << 20
