package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/auth"
	"github.com/antimatter-studios/vertrag/config"
)

// loginServer answers a login with whatever the test asked it to set, and
// records what it was sent so the request itself can be asserted on.
type loginServer struct {
	*httptest.Server
	method      string
	contentType string
	body        string
}

func newLoginServer(t *testing.T, respond func(w http.ResponseWriter)) *loginServer {
	t.Helper()
	server := &loginServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.method = r.Method
		server.contentType = r.Header.Get("Content-Type")
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		server.body = string(body)
		respond(w)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestObtainCookie(t *testing.T) {
	// Two cookies are set and only one authenticates, which is the case that
	// makes `cookie:` necessary rather than decorative.
	server := newLoginServer(t, func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "analytics", Value: "irrelevant"})
		http.SetCookie(w, &http.Cookie{Name: "jwt_token", Value: "the-real-one"})
		w.WriteHeader(http.StatusOK)
	})

	credential, err := auth.Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		Login:  config.Login{Method: "POST", Path: "/auth/login", Body: map[string]any{"username": "admin"}},
		Carry:  "cookie",
		Cookie: "jwt_token",
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}

	if want := "Cookie: jwt_token=the-real-one"; credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
	if server.method != "POST" {
		t.Errorf("login method = %q, want POST", server.method)
	}
	if server.contentType != "application/json" {
		t.Errorf("login Content-Type = %q, want application/json", server.contentType)
	}
	if !strings.Contains(server.body, `"username":"admin"`) {
		t.Errorf("login body = %q, want it to carry the configured credentials", server.body)
	}
}

func TestObtainCookieUnfilteredKeepsAll(t *testing.T) {
	server := newLoginServer(t, func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
		w.WriteHeader(http.StatusOK)
	})

	credential, err := auth.Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		Login: config.Login{Method: "POST", Path: "/auth/login"},
		Carry: "cookie",
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if want := "Cookie: a=1; b=2"; credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
}

func TestObtainBearerByDefault(t *testing.T) {
	server := newLoginServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"abc123"}`))
	})

	credential, err := auth.Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		Login: config.Login{Method: "POST", Path: "/auth/login"},
		Carry: "bearer",
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if want := "Authorization: Bearer abc123"; credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
}

func TestObtainHeaderTemplateReadsTheResponse(t *testing.T) {
	// The header is a runtime expression template, so a token that is not at
	// /token needs no new configuration key.
	server := newLoginServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"access_token":"nested-value"}}`))
	})

	credential, err := auth.Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		Login:  config.Login{Method: "POST", Path: "/auth/login"},
		Header: "X-Token: {$response.body#/data/access_token}",
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if want := "X-Token: nested-value"; credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
}

func TestObtainStaticHeaderSendsNoRequest(t *testing.T) {
	// A static API key must not require a login endpoint to exist.
	asked := false
	server := newLoginServer(t, func(w http.ResponseWriter) {
		asked = true
		w.WriteHeader(http.StatusOK)
	})

	credential, err := auth.Obtain(context.Background(), server.Client(), server.URL, config.Auth{
		Header: "X-API-Key: static-key",
	})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if want := "X-API-Key: static-key"; credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
	if asked {
		t.Error("a static credential sent a login request")
	}
}

func TestObtainUnconfiguredIsSilent(t *testing.T) {
	credential, err := auth.Obtain(context.Background(), http.DefaultClient, "http://example.invalid", config.Auth{})
	if err != nil {
		t.Fatalf("Obtain: %v", err)
	}
	if credential != "" {
		t.Errorf("credential = %q, want empty", credential)
	}
}

// The error cases matter more than the happy path: a run that fails to
// authenticate reports every transaction as failing, and the reader needs to be
// told which of those hundred failures is the actual cause.
func TestObtainErrors(t *testing.T) {
	tests := []struct {
		name     string
		respond  func(w http.ResponseWriter)
		settings config.Auth
		wants    []string
	}{
		{
			name: "the login is rejected",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"bad password"}`))
			},
			settings: config.Auth{Login: config.Login{Method: "POST", Path: "/auth/login"}, Carry: "cookie"},
			wants:    []string{"401", "bad password"},
		},
		{
			name:     "the named cookie is not the one set",
			respond:  func(w http.ResponseWriter) { http.SetCookie(w, &http.Cookie{Name: "session", Value: "x"}) },
			settings: config.Auth{Login: config.Login{Method: "POST", Path: "/auth/login"}, Carry: "cookie", Cookie: "jwt_token"},
			// The names actually set are in the message, because the fix is to
			// copy one of them into the config.
			wants: []string{"jwt_token", "session"},
		},
		{
			name:     "no cookie at all",
			respond:  func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
			settings: config.Auth{Login: config.Login{Method: "POST", Path: "/auth/login"}, Carry: "cookie"},
			wants:    []string{"no cookies"},
		},
		{
			name: "the token is not where it was expected",
			respond: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"jwt":"elsewhere"}`))
			},
			settings: config.Auth{Login: config.Login{Method: "POST", Path: "/auth/login"}, Carry: "bearer"},
			wants:    []string{"$response.body#/token", "elsewhere"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newLoginServer(t, test.respond)
			_, err := auth.Obtain(context.Background(), server.Client(), server.URL, test.settings)
			if err == nil {
				t.Fatal("Obtain succeeded, want an error")
			}
			for _, want := range test.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestObtainUnreachableServer(t *testing.T) {
	_, err := auth.Obtain(context.Background(), http.DefaultClient, "http://127.0.0.1:1", config.Auth{
		Login: config.Login{Method: "POST", Path: "/auth/login"},
		Carry: "cookie",
	})
	if err == nil {
		t.Fatal("Obtain succeeded against a closed port, want an error")
	}
	if !strings.Contains(err.Error(), "logging in") {
		t.Errorf("error %q does not say what failed", err)
	}
}
