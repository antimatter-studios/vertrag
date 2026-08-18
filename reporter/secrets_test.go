package reporter

import (
	"strings"
	"testing"
)

// TestALoginExchangeDoesNotCarryTheCredentialIntoARecording is the leak this
// exists to close.
//
// Header redaction never covered it: in the login exchange itself the password
// goes out in the body and the token comes back in the body, and a VCR cassette
// is written to be committed. So the one exchange most worth protecting was the
// one exchange fully exposed.
func TestALoginExchangeDoesNotCarryTheCredentialIntoARecording(t *testing.T) {
	t.Cleanup(ForgetSecrets)
	ForgetSecrets()

	RegisterSecretsIn(map[string]any{"username": "admin", "password": "hunter2-correct-horse"})
	RegisterSecret("eyJhbGciOiJIUzI1NiJ9.session-token-value")

	sent := RedactSecrets(`{"username":"admin","password":"hunter2-correct-horse"}`)
	if strings.Contains(sent, "hunter2-correct-horse") {
		t.Errorf("the posted password survived into the report: %s", sent)
	}
	back := RedactSecrets(`{"token":"eyJhbGciOiJIUzI1NiJ9.session-token-value","user":"admin"}`)
	if strings.Contains(back, "session-token-value") {
		t.Errorf("the returned token survived into the report: %s", back)
	}

	// The shape survives — a redacted report still has to be readable, and the
	// field names are what tell the reader what was sent.
	for _, want := range []string{`"password"`, `"token"`, Redacted} {
		if !strings.Contains(sent+back, want) {
			t.Errorf("redaction destroyed the readable shape; %q is gone:\n%s\n%s", want, sent, back)
		}
	}
}

// TestOnlyTheRegisteredValuesAreTouched pins the boundary that makes this
// acceptable at all. sanitize.go refuses to redact bodies by guesswork, and
// this must not quietly become guesswork: a body field called `password` that
// vertrag never supplied is somebody's payload, and hiding it would hide the
// very thing a failure needs to show.
func TestOnlyTheRegisteredValuesAreTouched(t *testing.T) {
	t.Cleanup(ForgetSecrets)
	ForgetSecrets()
	RegisterSecret("the-actual-credential")

	body := `{"password":"a-users-own-password","note":"the-actual-credential is here"}`
	got := RedactSecrets(body)

	if strings.Contains(got, "the-actual-credential") {
		t.Errorf("the registered value was not redacted: %s", got)
	}
	if !strings.Contains(got, "a-users-own-password") {
		t.Errorf("a value vertrag never supplied was redacted, on the strength of its field name: %s", got)
	}
}

// TestAShortCredentialIsRefusedRatherThanRedacted: redaction is a substring
// replacement, so a two-character secret would rewrite every unrelated body
// containing those two characters and the report would be worse than useless.
// Refusing is the safer failure, and RegisterSecret says so rather than
// silently taking it.
func TestAShortCredentialIsRefusedRatherThanRedacted(t *testing.T) {
	t.Cleanup(ForgetSecrets)
	ForgetSecrets()

	if RegisterSecret("ab") {
		t.Error("a two-character credential should be refused")
	}
	if got := RedactSecrets(`{"a":"ab","b":"absolutely"}`); got != `{"a":"ab","b":"absolutely"}` {
		t.Errorf("a refused credential still rewrote the body: %s", got)
	}
	if !RegisterSecret("long-enough-to-be-a-secret") {
		t.Error("a credential of real length should be taken")
	}
}

// TestTheLongestCredentialIsRedactedFirst guards an ordering bug: a token that
// contains a shorter registered value would otherwise have its tail left
// exposed by the shorter match landing first.
func TestTheLongestCredentialIsRedactedFirst(t *testing.T) {
	t.Cleanup(ForgetSecrets)
	ForgetSecrets()
	RegisterSecret("secretpass")
	RegisterSecret("secretpass-plus-the-rest-of-the-token")

	got := RedactSecrets(`{"t":"secretpass-plus-the-rest-of-the-token"}`)
	if strings.Contains(got, "plus-the-rest-of-the-token") {
		t.Errorf("the tail of the longer credential survived: %s", got)
	}
}

// TestNoSanitizeStillShowsTheCredential: --no-sanitize exists for the person
// debugging their own credential, and it has to mean the same thing here as it
// does for headers or it is a different flag wearing the same name.
func TestNoSanitizeStillShowsTheCredential(t *testing.T) {
	t.Cleanup(func() { ForgetSecrets(); SetSanitize(true) })
	ForgetSecrets()
	RegisterSecret("the-actual-credential")

	SetSanitize(false)
	if got := RedactSecrets(`{"t":"the-actual-credential"}`); !strings.Contains(got, "the-actual-credential") {
		t.Errorf("--no-sanitize did not show the credential: %s", got)
	}
}
