package main

import (
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// TestSupplierNamesSomethingThatWorks pins that the advice given for a missing
// credential is advice someone can act on.
//
// Telling a reader to use --header for a key that travels in the query would
// send them in circles: the flag cannot put it there. That is the case worth
// getting right, because it is the one where the obvious answer is wrong.
func TestSupplierNamesSomethingThatWorks(t *testing.T) {
	for _, test := range []struct {
		security compile.Security
		want     string
	}{
		{compile.Security{Type: "apiKey", In: "header", Parameter: "X-Api-Key"},
			"--header 'X-Api-Key: <value>'"},
		{compile.Security{Type: "http", Scheme: "bearer"},
			"--header 'Authorization: Bearer <token>'"},
		{compile.Security{Type: "http", Scheme: "basic"},
			"--header 'Authorization: Basic <base64>'"},
		{compile.Security{Type: "apiKey", In: "query", Parameter: "api_key"},
			"no flag supplies a credential in the query; a hook can set it"},
		{compile.Security{Type: "apiKey", In: "cookie", Parameter: "session"},
			"no flag supplies a credential in the cookie; a hook can set it"},
		{compile.Security{Type: "oauth2"},
			"a hook can set the credential this scheme needs"},
	} {
		if got := test.security.Supplier(); got != test.want {
			t.Errorf("Supplier() = %q, want %q", got, test.want)
		}
	}
}

// TestASuppliedCredentialIsNotReportedMissing pins that the note goes away once
// the run carries the header, so a correctly configured suite stays quiet.
func TestASuppliedCredentialIsNotReportedMissing(t *testing.T) {
	transactions := []compile.Transaction{{
		Security: []compile.Security{
			{Name: "ApiKeyHeader", Type: "apiKey", In: "header", Parameter: "X-Api-Key"},
			{Name: "BearerAuth", Type: "http", Scheme: "bearer"},
		},
	}}

	// Nothing supplied: both are missing.
	if got := missingCredentials(transactions, nil); len(got) != 2 {
		t.Errorf("got %d missing, want 2", len(got))
	}

	// The header name is matched case-insensitively, as HTTP requires.
	if got := missingCredentials(transactions, []string{"x-api-key: secret"}); len(got) != 1 {
		t.Errorf("got %d missing with the key supplied, want 1", len(got))
	}
	if got := missingCredentials(transactions, []string{"Authorization: Bearer t"}); len(got) != 1 {
		t.Errorf("got %d missing with the token supplied, want 1", len(got))
	}
	if got := missingCredentials(transactions,
		[]string{"X-Api-Key: k", "Authorization: Bearer t"}); len(got) != 0 {
		t.Errorf("got %d missing with both supplied, want 0", len(got))
	}
}
