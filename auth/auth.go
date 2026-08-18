// Package auth obtains the credential a run authenticates with.
//
// Authentication is the one piece of setup very nearly every suite needs and
// almost none of which is specific to the project: log in once, keep what came
// back, send it on everything afterwards. Dredd has no way to express that, so
// every suite writes it again as a hook file — which costs a worker process and
// a language runtime to perform three steps that do not vary between projects.
//
// What does vary stays in hooks. This package has no conditionals, reads nothing
// per-transaction, and runs exactly once before the first request.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/link"
)

// bearerToken is where `carry: bearer` looks for the token when the
// configuration does not say. It is by far the most common shape, and a config
// that has to state it every time is a config stating the obvious.
const bearerToken = "Authorization: Bearer {$response.body#/token}"

// Obtain performs the login exchange and returns the credential as a
// `Name: value` header line, or "" when no authentication was configured.
//
// A failure here is returned rather than warned about: a run that could not log
// in will fail every authenticated transaction, and reporting a hundred 401s
// tells the reader nothing about their API.
func Obtain(ctx context.Context, client *http.Client, endpoint string, settings config.Auth) (string, error) {
	if !settings.Configured() {
		return "", nil
	}

	// The client credentials grant, when configured: a token endpoint rather
	// than a login path, and a form rather than a JSON body.
	if settings.OAuth2.Configured() {
		return obtainOAuth2(ctx, client, endpoint, settings.OAuth2)
	}

	// A static credential — an API key, a token from the environment — needs no
	// exchange, and demanding a login endpoint for one would be absurd.
	if settings.Login.Path == "" {
		return settings.Header, nil
	}

	response, body, err := logIn(ctx, client, endpoint, settings.Login)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("logging in at %s: the server answered %s\n%s",
			settings.Login.Path, response.Status, excerpt(body))
	}

	return credential(response, body, settings)
}

// credential reads the credential out of the login response.
func credential(response *http.Response, body []byte, settings config.Auth) (string, error) {
	// A cookie cannot be addressed by a runtime expression: Set-Cookie may
	// appear many times, and the value wanted is one attribute of one of them.
	if strings.EqualFold(settings.Carry, "cookie") {
		return cookieHeader(response, settings.Cookie)
	}

	template := settings.Header
	if template == "" {
		template = bearerToken
	}

	exchange := link.Exchange{
		Method:         response.Request.Method,
		URL:            response.Request.URL.String(),
		StatusCode:     fmt.Sprint(response.StatusCode),
		ResponseBody:   string(body),
		ResponseHeader: firstValues(response.Header),
	}

	value, ok := link.Evaluate(template, exchange)
	if !ok {
		return "", fmt.Errorf(
			"the login response has nothing at %q\n%s", template, excerpt(body))
	}
	text := fmt.Sprint(value)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("the login response gave an empty credential for %q", template)
	}
	return text, nil
}

// cookieHeader builds a Cookie header from the login response's Set-Cookie
// headers, optionally keeping only one of them by name.
func cookieHeader(response *http.Response, wanted string) (string, error) {
	var pairs []string
	for _, cookie := range response.Cookies() {
		if wanted != "" && cookie.Name != wanted {
			continue
		}
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}

	if len(pairs) == 0 {
		if wanted != "" {
			return "", fmt.Errorf(
				"the login response set no cookie named %q (it set %s)",
				wanted, describeCookies(response))
		}
		return "", fmt.Errorf("the login response set no cookies")
	}
	return "Cookie: " + strings.Join(pairs, "; "), nil
}

func logIn(ctx context.Context, client *http.Client, endpoint string, login config.Login) (*http.Response, []byte, error) {
	var payload io.Reader
	contentType := ""
	if len(login.Body) > 0 {
		encoded, err := json.Marshal(login.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding the login body: %w", err)
		}
		payload = bytes.NewReader(encoded)
		contentType = "application/json"
	}

	url := strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(login.Path, "/")
	request, err := http.NewRequestWithContext(ctx, login.Method, url, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("building the login request: %w", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("logging in at %s: %w", url, err)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		response.Body.Close()
		return nil, nil, fmt.Errorf("reading the login response: %w", err)
	}
	return response, body, nil
}

// describeCookies names the cookies that were set, so a mismatched name is one
// glance to fix rather than a round of guessing.
func describeCookies(response *http.Response) string {
	var names []string
	for _, cookie := range response.Cookies() {
		names = append(names, cookie.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func firstValues(header http.Header) map[string]string {
	values := make(map[string]string, len(header))
	for name := range header {
		values[name] = header.Get(name)
	}
	return values
}

// excerpt trims a body down to something worth putting in an error message.
func excerpt(body []byte) string {
	const most = 400
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(the response had no body)"
	}
	if len(text) > most {
		text = text[:most] + "…"
	}
	return text
}
