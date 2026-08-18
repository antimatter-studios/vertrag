package runner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// Judging a GraphQL response is a different job from judging a REST one, and
// almost none of the ordinary checks carry over.
//
// A GraphQL endpoint answers 200 to very nearly everything. A query naming a
// field that does not exist, a resolver that threw, an unauthenticated caller,
// and a request served perfectly are all 200 with different bodies — the
// GraphQL over HTTP specification only permits a non-200 for requests that
// could not be processed at all. So "the status matched" proves close to
// nothing here: a run that checked only the status would pass, green and
// silent, against a server answering every single query with an error. That is
// not a hypothetical failure mode, it is the default one — a schema change, a
// missing resolver and an expired credential all look exactly like it.
//
// What is worth checking is the body, and there the specification is precise
// enough to check against:
//
//   - `errors` present and non-empty means the request failed, whatever the
//     status said, and the messages are the diagnostic.
//   - Neither `data` nor `errors` is not a GraphQL response at all.
//   - `data` must answer the question that was put: the keys the selection set
//     asked for, non-null where the schema promised non-null.
//
// The findings go into Result.Errors rather than Result.Beyond, which is where
// the checks Dredd does not make normally land. Beyond exists so a project
// arriving from Dredd can tell a new finding from a regression — and Dredd
// cannot test GraphQL at all, so there is no "Dredd would have missed this" to
// draw here. These are the contract, and the contract belongs in Errors.

// graphqlError is one entry of the response's `errors` array. Only the fields
// worth reporting are read: the message, where in the query it happened, and
// the error code servers conventionally put in extensions.
type graphqlError struct {
	Message    string `json:"message"`
	Path       []any  `json:"path"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// graphqlResponseFindings judges a GraphQL response, or returns nothing when
// this was not a GraphQL transaction.
func graphqlResponseFindings(expectation *compile.GraphQL, expected, actual validate.Message) []string {
	if expectation == nil {
		return nil
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(actual.Body), &document); err != nil {
		// A body that will not parse is only worth reporting when the response
		// was otherwise the one expected. A 502 carrying a proxy's HTML error
		// page is one fault, and reporting it as both a status mismatch and a
		// malformed GraphQL response is the cascade that teaches a reader to
		// skim: one root cause, one finding.
		if !statusMatches(expected, actual) {
			return nil
		}
		return []string{fmt.Sprintf(
			"the response body is not a JSON object, so it cannot be a GraphQL response: %v", err)}
	}

	errorsRaw, hasErrors := document["errors"]
	dataRaw, hasData := document["data"]

	if hasErrors {
		if findings := graphqlErrorFindings(errorsRaw); len(findings) > 0 {
			if len(expectation.Possessed) > 0 {
				// This query passes an identifier vertrag made up, because a
				// GraphQL schema states no example request and `userById(id:
				// ID!)` cannot be asked without one. Every legal id is well
				// formed and which ones EXIST is the server's data rather than
				// its contract, so a server answering "no such user" is right
				// and reporting it would be vertrag failing the API for a row
				// nobody ever created. It is the same exemption a path
				// parameter's 404 already gets, and the reasoning is written
				// out at refusals.isLogin: generation can produce anything the
				// caller must SHAPE and nothing they must POSSESS.
				//
				// What it costs is real and is not hidden: a genuinely broken
				// resolver behind such a field goes unreported here. The
				// compiler names every operation in this position up front —
				// see GraphQLResult.Notes — so the exemption is visible before
				// the results rather than inferred from their absence.
				return nil
			}
			// The shape of `data` is deliberately not checked as well. A failed
			// field is null by specification and its children are absent, so
			// walking the selection here would bury the server's own
			// explanation under a dozen findings that all restate it.
			return findings
		}
	}

	if !hasData && !hasErrors {
		return []string{"the response carries neither `data` nor `errors`; a GraphQL response must carry one of them"}
	}
	if !hasData {
		return nil
	}

	var data any
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return []string{fmt.Sprintf("the response's `data` is not readable JSON: %v", err)}
	}
	if data == nil {
		// `data` is null only when the request failed, and a failed request
		// carries errors — which this one did not, or the branch above would
		// have returned.
		return []string{"the response's `data` is null and it reported no errors, " +
			"which leaves nothing to say what went wrong"}
	}

	object, ok := data.(map[string]any)
	if !ok {
		return []string{"the response's `data` is not an object, so it cannot hold the fields the query selected"}
	}
	return graphqlSelectionFindings("", expectation.Data, object)
}

// graphqlErrorFindings turns the `errors` array into one finding per error.
//
// An empty array is itself a finding: the specification says that if the entry
// is present it must not be empty, and a server emitting `"errors": []`
// alongside good data is one whose error handling nobody has looked at.
func graphqlErrorFindings(raw json.RawMessage) []string {
	var errors []graphqlError
	if err := json.Unmarshal(raw, &errors); err != nil {
		return []string{fmt.Sprintf("the response's `errors` is not a list of GraphQL errors: %v", err)}
	}
	if len(errors) == 0 {
		return []string{"the response carries an empty `errors` array; a GraphQL response that " +
			"reports errors at all must report at least one"}
	}

	findings := make([]string, 0, len(errors))
	for _, failure := range errors {
		message := failure.Message
		if message == "" {
			message = "(the server gave no message)"
		}
		finding := "the server answered with a GraphQL error: " + message
		if code := failure.Extensions.Code; code != "" {
			finding += " [" + code + "]"
		}
		if path := graphqlPath(failure.Path); path != "" {
			finding += " (at " + path + ")"
		}
		findings = append(findings, finding)
	}
	return findings
}

// graphqlPath renders an error's path the way the findings render their own,
// so the two can be read against each other.
func graphqlPath(path []any) string {
	var out strings.Builder
	for _, segment := range path {
		switch value := segment.(type) {
		case string:
			if out.Len() > 0 {
				out.WriteString(".")
			}
			out.WriteString(value)
		case float64:
			out.WriteString(fmt.Sprintf("[%d]", int(value)))
		default:
			if out.Len() > 0 {
				out.WriteString(".")
			}
			out.WriteString(fmt.Sprint(value))
		}
	}
	return out.String()
}

// graphqlSelectionFindings checks one object against the selection that asked
// for it.
func graphqlSelectionFindings(path string, selections []compile.GraphQLSelection, object map[string]any) []string {
	var findings []string
	for _, selection := range selections {
		value, present := object[selection.Key]
		where := graphqlJoin(path, selection.Key)

		if !present {
			// A key that came from an inline fragment is present only when the
			// object turned out to be that concrete type, so its absence says
			// nothing. Requiring it would fail every response that returned
			// the other implementation.
			if selection.Conditional {
				continue
			}
			findings = append(findings, fmt.Sprintf(
				"the query asked for `%s` and the response does not carry it", where))
			continue
		}
		findings = append(findings, graphqlValueFindings(where, selection, value)...)
	}
	return findings
}

// graphqlValueFindings checks one value against what the schema said about it.
func graphqlValueFindings(where string, selection compile.GraphQLSelection, value any) []string {
	if value == nil {
		if selection.NonNull {
			return []string{fmt.Sprintf(
				"`%s` is null, but the schema declares it %s", where, selection.Type)}
		}
		// Legitimately null, and there is nothing underneath a null to check.
		return nil
	}

	if selection.ListDepth > 0 {
		return graphqlListFindings(where, selection, value, selection.ListDepth)
	}
	if len(selection.Fields) == 0 {
		// A leaf. Its type is not checked against the schema's scalar: a
		// custom scalar can be any JSON at all, and the ones that are not —
		// Int, String — are already what the server's own serialiser produced.
		return nil
	}

	object, ok := value.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf(
			"`%s` is not an object, but the query selected fields from it (the schema declares it %s)",
			where, selection.Type)}
	}
	return graphqlSelectionFindings(where, selection.Fields, object)
}

// graphqlListFindings checks a list value.
//
// Every element's nullability is checked, because that is the fault that
// genuinely varies down a list — one null row in a `[User!]!` is exactly the
// bug this finds. The SHAPE, though, is checked against the first element
// only: every element of a list comes from one resolver and one type, so a
// missing field is one fault, and reporting it once per row would bury a
// hundred-row response's real findings under a hundred copies of the same one.
func graphqlListFindings(where string, selection compile.GraphQLSelection, value any, depth int) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{fmt.Sprintf(
			"`%s` is not a list, but the schema declares it %s", where, selection.Type)}
	}

	var findings []string
	for i, item := range items {
		at := fmt.Sprintf("%s[%d]", where, i)
		if item == nil {
			// Only the innermost level carries the element's own nullability.
			// A schema nesting lists deeply enough for the intermediate levels
			// to differ is rare enough that checking them would be more
			// machinery than the finding is worth.
			if depth == 1 && selection.ElementNonNull {
				findings = append(findings, fmt.Sprintf(
					"`%s` is null, but the schema declares it %s", at, selection.Type))
			}
			continue
		}
		if depth > 1 {
			findings = append(findings, graphqlListFindings(at, selection, item, depth-1)...)
			continue
		}
		if len(selection.Fields) == 0 || i > 0 {
			continue
		}
		object, ok := item.(map[string]any)
		if !ok {
			findings = append(findings, fmt.Sprintf(
				"`%s` is not an object, but the query selected fields from it (the schema declares it %s)",
				at, selection.Type))
			continue
		}
		findings = append(findings, graphqlSelectionFindings(at, selection.Fields, object)...)
	}
	return findings
}

func graphqlJoin(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
