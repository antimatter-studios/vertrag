package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/antimatter-studios/vertrag/config"
)

// OAuth2's client credentials grant is the one flow a test suite can perform
// on its own: a service asking for a token in its own name, with no user, no
// browser and no consent screen. It is two lines of HTTP — post the client's
// identity to the token endpoint, read `access_token` out of the reply — and
// expressing it as a hook file costs a worker process and a language runtime
// to do that.
//
// The interactive grants are deliberately absent. authorization_code, PKCE
// and the device flow all require a human at a browser; no headless suite
// performs them, and a test environment that cannot mint a token
// non-interactively has a problem no tester should paper over.

// obtainOAuth2 performs the client credentials grant and returns the
// credential as a `Name: value` header line.
func obtainOAuth2(ctx context.Context, client *http.Client, endpoint string, settings config.OAuth2) (string, error) {
	secret, err := clientSecret(settings)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", settings.ClientID)
	form.Set("client_secret", secret)
	if len(settings.Scopes) > 0 {
		form.Set("scope", strings.Join(settings.Scopes, " "))
	}

	address := tokenURL(endpoint, settings.TokenURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building the token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("requesting a token from %s: %w", address, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading the token response: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		// The error body is the useful part: OAuth2 servers say
		// `invalid_client` or `invalid_scope`, which names the fix.
		return "", fmt.Errorf("requesting a token from %s: the server answered %s\n%s",
			address, response.Status, excerpt(body))
	}

	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("the token response is not JSON: %w\n%s", err, excerpt(body))
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", fmt.Errorf("the token response carries no access_token\n%s", excerpt(body))
	}

	// RFC 6749 says the type is case-insensitive and "Bearer" is what every
	// server means; a server that omits it means Bearer too.
	kind := strings.TrimSpace(token.TokenType)
	if kind == "" || strings.EqualFold(kind, "bearer") {
		kind = "Bearer"
	}
	return "Authorization: " + kind + " " + token.AccessToken, nil
}

// clientSecret reads the secret, preferring the environment.
//
// A secret written into a configuration file is a secret in version control,
// so the environment is the documented way and the file is the fallback for
// the CI systems that generate their config with the secret already in it.
func clientSecret(settings config.OAuth2) (string, error) {
	if settings.ClientSecretEnv != "" {
		secret := os.Getenv(settings.ClientSecretEnv)
		if strings.TrimSpace(secret) == "" {
			return "", fmt.Errorf("the environment variable %s is empty, and it is where auth.oauth2 expects the client secret",
				settings.ClientSecretEnv)
		}
		return secret, nil
	}
	if settings.ClientSecret != "" {
		return settings.ClientSecret, nil
	}
	return "", fmt.Errorf("auth.oauth2 needs a client secret: set `client-secret-env` to the name of an environment variable holding it, or `client-secret` to the value")
}

// tokenURL resolves the token endpoint, which may be absolute — an identity
// provider is often not the API under test — or a path on the endpoint.
func tokenURL(endpoint, token string) string {
	if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
		return token
	}
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(token, "/")
}
