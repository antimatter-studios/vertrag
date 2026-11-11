package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

func TestLoginSessionUsesTheAdvertisedLifetimeAcrossLongWaits(t *testing.T) {
	for _, lifetime := range []int{900, 120} {
		t.Run(fmt.Sprint(lifetime), func(t *testing.T) {
			logins := 0
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
				}
				bodies = append(bodies, string(body))
				logins++
				fmt.Fprintf(response, `{"token":"session-%d","expires_in":%d}`, logins, lifetime)
			}))
			defer server.Close()
			now := time.Unix(100000, 0)
			session := &loginSession{
				client: server.Client(), endpoint: server.URL, now: func() time.Time { return now },
				settings: config.Auth{
					Login: config.Login{Method: "POST", Path: "/login", Body: map[string]any{"email": "owner@example.test", "password": "disposable"}},
					Carry: "bearer", ExpiresIn: "{$response.body#/expires_in}",
				},
			}
			initial, err := session.current(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			boundary := time.Duration(lifetime)*time.Second - min(time.Minute, time.Duration(lifetime)*time.Second/2)
			now = now.Add(boundary - time.Nanosecond)
			cached, err := session.current(t.Context())
			if err != nil || cached != initial || logins != 1 {
				t.Fatalf("session renewed before its safe boundary: logins=%d, error=%v", logins, err)
			}
			now = now.Add(time.Nanosecond)
			renewed, err := session.current(t.Context())
			if err != nil || renewed == initial || logins != 2 {
				t.Fatalf("session was not renewed at its safe boundary: logins=%d, error=%v", logins, err)
			}
			now = time.Unix(103601, 0)
			current, err := session.current(t.Context())
			if err != nil || current == renewed || logins != 3 {
				t.Fatalf("session was not renewed after the hour wait: logins=%d, error=%v", logins, err)
			}
			if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
				t.Error("renewal changed the login principal or body")
			}
		})
	}
}

func TestLoginSessionRefusesMissingInvalidAndConsumedLifetimes(t *testing.T) {
	for _, lifetime := range []string{"", `,"expires_in":0`, `,"expires_in":-1`, `,"expires_in":true`, `,"expires_in":"900"`, `,"expires_in":1.5`, `,"expires_in":1e20`, `,"expires_in":null`} {
		t.Run(lifetime, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				fmt.Fprintf(response, `{"token":"secret-not-for-errors"%s}`, lifetime)
			}))
			defer server.Close()
			_, renew, err := ObtainRenewable(t.Context(), server.Client(), server.URL, config.Auth{
				Login: config.Login{Path: "/login"}, Carry: "bearer", ExpiresIn: "$response.body#/expires_in",
			})
			if err == nil || renew != nil || !strings.Contains(err.Error(), "positive integer access lifetime") || strings.Contains(err.Error(), "secret-not-for-errors") {
				t.Fatalf("invalid lifetime accepted or leaked its credential: %v", err)
			}
		})
	}
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("clock-rollback=%t", rollback), func(t *testing.T) {
			now := time.Unix(100000, 0)
			logins := 0
			slow := false
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				logins++
				if slow {
					now = now.Add(70 * time.Second)
				}
				fmt.Fprintf(response, `{"token":"session-%d","expires_in":120}`, logins)
			}))
			defer server.Close()
			session := &loginSession{client: server.Client(), endpoint: server.URL, now: func() time.Time { return now },
				settings: config.Auth{Login: config.Login{Path: "/login"}, Carry: "bearer", ExpiresIn: "$response.body#/expires_in"}}
			if _, err := session.current(t.Context()); err != nil {
				t.Fatal(err)
			}
			if rollback {
				now = now.Add(-time.Second)
				if _, err := session.current(t.Context()); err != nil || logins != 2 {
					t.Fatalf("rollback reused the previous clock's session: logins=%d, error=%v", logins, err)
				}
			}
			now = now.Add(time.Hour)
			slow = true
			if _, err := session.current(t.Context()); err == nil || !strings.Contains(err.Error(), "consumed its safe access lifetime") {
				t.Fatalf("slow login was accepted: %v", err)
			}
		})
	}
}

func TestLoginSessionRenewalIsSerializedAndRefusalNeverFallsBack(t *testing.T) {
	now := time.Unix(100000, 0)
	logins := 0
	refuse := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		logins++
		if refuse {
			response.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(response, `{"token":"secret-not-for-errors"}`)
			return
		}
		fmt.Fprintf(response, `{"token":"session-%d","expires_in":900}`, logins)
	}))
	defer server.Close()
	session := &loginSession{client: server.Client(), endpoint: server.URL, now: func() time.Time { return now },
		settings: config.Auth{Login: config.Login{Path: "/login"}, Carry: "bearer", ExpiresIn: "$response.body#/expires_in"}}
	if _, err := session.current(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := session.current(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if logins != 2 {
		t.Fatalf("concurrent renewal made %d login calls, want two total", logins)
	}
	now = now.Add(time.Hour)
	refuse = true
	header, err := session.current(t.Context())
	if err == nil || header != "" || strings.Contains(err.Error(), "secret-not-for-errors") {
		t.Fatalf("refused renewal fell back to an old credential or leaked a secret: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	previous := logins
	if _, err := session.current(ctx); err != context.Canceled || logins != previous {
		t.Fatal("cancelled renewal made a login request")
	}
}
