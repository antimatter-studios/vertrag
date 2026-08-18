package openapi3

import (
	"fmt"
	"strings"
)

// Webhooks are the one part of an OpenAPI description vertrag reads in full and
// deliberately does not send.
//
// 3.1 added `webhooks`: a map of name to Path Item, the same shape as `paths`
// and pointing the other way. A path is a request the caller sends to the API;
// a webhook is a request the API sends to a receiver belonging to the caller,
// and the description carries no address for that receiver — there is nothing
// in the document that could be one, because whose receiver it is depends on
// who subscribed.
//
// vertrag is a client: it sends a request and judges the response. A webhook
// inverts exactly that, so there were three answers and only one of them is
// honest.
//
// Compiling them into transactions and sending them like anything else tests
// the reader's own receiver when `--endpoint` happens to point at it. But
// `--endpoint` names the API under test, which is the whole premise of a run,
// so the ordinary case is that the requests go to the API — an API being sent
// requests it never said it would answer, with whatever came back reported as
// a contract result. A 404 from the API would read as the webhook failing, and
// a webhook named `accountDeleted` POSTed at a real service is worse than a
// misleading report. Nothing in the description separates the two cases, which
// is the argument compile.WithheldMutation already makes about a mutation: when
// the destructive reading and the harmless one are equally reachable and the
// document does not distinguish them, the tool does not choose for you.
//
// Dropping them is the other half of the mistake and the worse half, because it
// is the failure this repository keeps hunting: `webhooks` was reported as an
// invalid key — the message meaning "this is not OpenAPI at all" — and the Path
// Items under it were never validated, never counted and never mentioned. A
// description whose entire surface is webhooks compiled to nothing, ran
// nothing, and reported a pass.
//
// So they are read: every Path Item under `webhooks` is validated exactly as
// one under `paths` is, so the schemas, parameters and responses of a
// webhooks-only description are checked as thoroughly as anyone else's. And
// every operation is named and counted in a diagnostic saying it was not sent,
// so no run can imply it covered them. Sending them is a mode of its own — an
// endpoint naming the receiver, and vertrag playing the API rather than the
// client — and until that exists, saying so is what an honest run does.

// validateWebhooks checks the Path Items under `webhooks` and reports the
// operations that were not sent.
func (d *document) validateWebhooks(webhooks node) []annotation {
	if !webhooks.IsMapping() {
		return nil
	}

	var out []annotation
	var names []string
	for _, member := range webhooks.Entries() {
		out = append(out, d.validatePathItem(member.Value)...)
		names = append(names, d.webhookOperationNames(member)...)
	}

	if len(names) == 0 {
		// A `webhooks` map that describes no operation at all — empty, or Path
		// Items with nothing under them — withholds nothing, so there is
		// nothing to declare. Its Path Items were still checked above.
		return out
	}

	return append(out, d.at(annotation{
		class: "warning",
		message: fmt.Sprintf(
			"%s read and checked but not sent — a webhook is a request the API sends to a "+
				"receiver of yours, and vertrag is a client aimed at the API under test, with "+
				"no receiver to be and no address for one in the description: %s",
			webhookSubject(len(names)), strings.Join(names, ", ")),
	}, webhooks))
}

// webhookSubject is the count, phrased as the subject of the sentence above.
//
// The count leads because the count is what a reader checks against their own
// expectation: "eight of these were not sent" is read, and "some operations
// were not sent" is not.
func webhookSubject(count int) string {
	if count == 1 {
		return "one of the description's webhook operations was"
	}
	return fmt.Sprintf("%d of the description's webhook operations were", count)
}

// webhookOperationNames names every operation one webhook declares.
//
// A webhook's Path Item holds operations in the same three places a path's does
// — a method field, 3.2's `query`, and 3.2's `additionalOperations` — and is
// resolved first, because a webhook is idiomatically a `$ref` into
// `components.pathItems` and an unresolved one would be listed as a webhook
// with no operations, which reads as nothing being withheld.
func (d *document) webhookOperationNames(webhook entry) []string {
	pathItem := d.Resolve(webhook.Value)
	if !pathItem.IsMapping() {
		return nil
	}

	name := webhook.Key.Str()
	var out []string
	for _, member := range pathItem.Entries() {
		if d.isOperationKey(member.Key.Str()) {
			out = append(out, name+" > "+strings.ToUpper(member.Key.Str()))
		}
	}
	// The method of an additional operation is the key as written, because that
	// is the spelling the specification says goes on the wire.
	for _, member := range pathItem.Get(additionalOperationsKey).Entries() {
		out = append(out, name+" > "+member.Key.Str())
	}
	return out
}
