// Package shape records the outline of the response bodies a run received, and
// reports where one operation answered a single status, in a single media type,
// with bodies no one client parser could read.
//
// The defect it exists for is FastAPI's, and it is ordinary rather than exotic.
// A handler that translates a domain `ValueError` raises 422 with `detail` as a
// string; the framework's own request validation raises 422 — same operation,
// same media type, nothing in the description telling them apart — with
// `detail` as an array of error objects. Any service that turns domain errors
// into 422 has it, because that is FastAPI's default behaviour meeting
// ordinary application code.
//
// It is a shape only a contract tester can see, and only within one run: the
// examples phase provokes the handler's version and the probing phases provoke
// the framework's, and nothing in a normal test suite meets both in the same
// breath.
package shape

import (
	"encoding/json"
	"sort"
	"strings"
)

// The JSON types a body can hold at a path. They are the whole vocabulary: a
// client parser branches on these and on nothing finer, so `1` and `1.5` are
// one kind and `true` and `false` are one kind.
const (
	Object  = "object"
	Array   = "array"
	String  = "string"
	Number  = "number"
	Boolean = "boolean"
)

// Bounds on reading one body's outline.
//
// A response is whatever the server chose to send, and a probing phase provokes
// thousands of them. Neither bound can change a verdict into a wrong one:
// truncating an outline only drops paths, and a difference needs a path present
// in two bodies — so what these cost is a report this failed to make, never one
// it made wrongly. They exist so a server answering with a megabyte of
// generated keys cannot make a run's bookkeeping its dominant cost.
const (
	maxNodes = 5000
	maxDepth = 64
)

// outline maps each path in a body to the JSON type found there.
//
// A map rather than a tree, because the only question ever asked of it is "what
// kind sits at this path", and the comparison that matters is between two
// bodies at the SAME path — which two trees could answer only by being walked
// in step.
type outline map[string]string

// outlineOf reads a body's outline, reporting whether it was JSON at all.
//
// A body that does not parse is refused rather than given an outline of its
// own. It is a defect, but a different one, and "unparseable versus object" is
// not the finding this package exists to make.
func outlineOf(body string) (outline, bool) {
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil, false
	}
	out := outline{}
	walk(out, map[string]bool{}, "", value, 0)
	return out, true
}

// walk records the kind at each path.
//
// Object keys are visited in sorted order so that a body truncated by maxNodes
// is truncated the same way every time. Two runs of one suite would otherwise
// produce outlines differing by whichever keys Go's map iteration reached
// first, which is a report nobody can reproduce.
func walk(out outline, mixed map[string]bool, path string, value any, depth int) {
	if depth > maxDepth || len(out) >= maxNodes {
		return
	}

	kind := kindOf(value)
	// A null records nothing at all — see Compatible for why null is read as
	// absence.
	if kind == "" {
		return
	}
	record(out, mixed, path, kind)

	switch v := value.(type) {
	case map[string]any:
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			walk(out, mixed, path+"/"+name, v[name], depth+1)
		}

	case []any:
		// Every element folds onto ONE path rather than one path per index.
		//
		// Indexes would make an outline as long as its array, and would compare
		// element 0 of a one-item response against element 0 of a hundred-item
		// one — two unrelated things. Folding asks the question that matters
		// instead: does this array hold the sort of element it held last time.
		for _, element := range v {
			walk(out, mixed, path+"/[]", element, depth+1)
		}
	}
}

// record writes a kind at a path, and drops the path entirely when one body has
// already used it for two different kinds.
//
// That can only happen through a folded array, and dropping is the right answer
// there: a server that sends `[1, "two"]` in a single response has declared
// that path polymorphic itself. Comparing it against another response tells the
// reader nothing they cannot already see in the one, so the path is removed
// rather than reported.
func record(out outline, mixed map[string]bool, path, kind string) {
	if mixed[path] {
		return
	}
	if seen, ok := out[path]; ok && seen != kind {
		delete(out, path)
		mixed[path] = true
		return
	}
	out[path] = kind
}

func kindOf(value any) string {
	switch value.(type) {
	case map[string]any:
		return Object
	case []any:
		return Array
	case string:
		return String
	case float64, json.Number:
		return Number
	case bool:
		return Boolean
	default:
		// nil, which is JSON null.
		return ""
	}
}

// isJSON reports whether a media type carries JSON.
//
// Only JSON is given an outline. A `text/csv` or `image/png` body has no shape
// this could compare, and guessing at one would report the difference between
// two pictures.
func isJSON(media string) bool {
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

// baseMediaType strips the parameters, so `application/json; charset=utf-8`
// and `application/json` are one media type rather than two.
func baseMediaType(value string) string {
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// contentType reads a response's content type, whatever case the header name
// arrived in. The runner lowercases them, but this package is reachable from
// anywhere and a header map is not a promise.
func contentType(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, "content-type") {
			return value
		}
	}
	return ""
}
