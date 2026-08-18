package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

// tokenServer is an OAuth2 token endpoint that checks the grant the way a
// real one does, so a test proving the credential arrives also proves the
// request was well formed.
func tokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if media := r.Header.Get("Content-Type"); !strings.HasPrefix(media, "application/x-www-form-urlencoded") {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("client_id") != "the-client" ||
			r.PostForm.Get("client_secret") != "the-secret" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok-123","token_type":"Bearer","expires_in":3600,` +
			`"scope":"` + r.PostForm.Get("scope") + `"}`))
	}))
}

func TestClientCredentialsGrantObtainsABearerToken(t *testing.T) {
	server := tokenServer(t)
	defer server.Close()
	t.Setenv("VERTRAG_TEST_SECRET", "the-secret")

	credential, err := Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		OAuth2: config.OAuth2{
			TokenURL:        "/oauth/token",
			ClientID:        "the-client",
			ClientSecretEnv: "VERTRAG_TEST_SECRET",
			Scopes:          []string{"read", "write"},
		},
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if credential != "Authorization: Bearer tok-123" {
		t.Errorf("credential = %q", credential)
	}
}

// TestTheSecretComesFromTheEnvironment pins the reason the env form exists: a
// secret in a configuration file is a secret in version control. The literal
// form still works, for CI that writes its config with the secret inside.
func TestTheSecretComesFromTheEnvironment(t *testing.T) {
	server := tokenServer(t)
	defer server.Close()

	// Named but unset is an error, not a silent empty secret sent to the
	// provider — which would fail as invalid_client and send the reader
	// looking at their credentials rather than their environment.
	_, err := Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		OAuth2: config.OAuth2{TokenURL: "/oauth/token", ClientID: "the-client", ClientSecretEnv: "VERTRAG_TEST_ABSENT"},
	})
	if err == nil || !strings.Contains(err.Error(), "VERTRAG_TEST_ABSENT") {
		t.Errorf("err = %v, want the empty environment variable named", err)
	}

	// The literal form works.
	credential, err := Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		OAuth2: config.OAuth2{TokenURL: "/oauth/token", ClientID: "the-client", ClientSecret: "the-secret"},
	})
	if err != nil || credential != "Authorization: Bearer tok-123" {
		t.Errorf("literal secret: credential = %q, err = %v", credential, err)
	}

	// No secret at all says which of the two keys to set.
	_, err = Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		OAuth2: config.OAuth2{TokenURL: "/oauth/token", ClientID: "the-client"},
	})
	if err == nil || !strings.Contains(err.Error(), "client-secret-env") {
		t.Errorf("err = %v, want both spellings offered", err)
	}
}

// TestARefusedGrantSaysWhatTheProviderSaid: OAuth2 servers answer with a
// machine-readable reason — invalid_client, invalid_scope — and that word is
// the whole diagnosis. Swallowing it for a bare status would throw away the
// one thing that names the fix.
func TestARefusedGrantSaysWhatTheProviderSaid(t *testing.T) {
	server := tokenServer(t)
	defer server.Close()

	_, err := Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		OAuth2: config.OAuth2{TokenURL: "/oauth/token", ClientID: "wrong", ClientSecret: "wrong"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("err = %v, want the provider's own reason", err)
	}
}

// TestAnAbsoluteTokenURLIsUsedAsGiven: the identity provider is usually not
// the API under test, so a token URL that names its own host must not be
// appended to the endpoint.
func TestAnAbsoluteTokenURLIsUsedAsGiven(t *testing.T) {
	provider := tokenServer(t)
	defer provider.Close()

	credential, err := Obtain(context.Background(), provider.Client(), "http://api.example.invalid",
		config.Auth{OAuth2: config.OAuth2{
			TokenURL: provider.URL + "/oauth/token", ClientID: "the-client", ClientSecret: "the-secret",
		}})
	if err != nil {
		t.Fatalf("an absolute token URL should be used as given: %v", err)
	}
	if credential != "Authorization: Bearer tok-123" {
		t.Errorf("credential = %q", credential)
	}
	if _, err := url.Parse(provider.URL); err != nil {
		t.Fatal(err)
	}
}
