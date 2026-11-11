package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/link"
)

type loginSession struct {
	mutex    sync.Mutex
	client   *http.Client
	endpoint string
	settings config.Auth
	now      func() time.Time
	header   string
	started  time.Time
	renewAt  time.Time
}

func ObtainRenewable(ctx context.Context, client *http.Client, endpoint string, settings config.Auth) (string, func(context.Context) (string, error), error) {
	if settings.ExpiresIn == "" {
		header, err := Obtain(ctx, client, endpoint, settings)
		return header, nil, err
	}
	if settings.Login.Path == "" || settings.OAuth2.Configured() {
		return "", nil, fmt.Errorf("auth.expires-in requires a login exchange, not a static or OAuth2 credential")
	}
	expression := strings.TrimSuffix(strings.TrimPrefix(settings.ExpiresIn, "{"), "}")
	if !strings.HasPrefix(expression, "$response.body#/") {
		return "", nil, fmt.Errorf("auth.expires-in must name a login response body field")
	}
	session := &loginSession{client: client, endpoint: endpoint, settings: settings, now: time.Now}
	encoded, err := json.Marshal(settings.Login.Body)
	if err != nil {
		return "", nil, fmt.Errorf("encoding the renewable login body")
	}
	if err := json.Unmarshal(encoded, &session.settings.Login.Body); err != nil {
		return "", nil, fmt.Errorf("copying the renewable login body")
	}
	header, err := session.current(ctx)
	if err != nil {
		return "", nil, err
	}
	return header, session.current, nil
}

func (session *loginSession) current(ctx context.Context) (string, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := session.now()
	if session.header != "" && !now.Before(session.started) && now.Before(session.renewAt) {
		return session.header, nil
	}
	started := now
	response, body, err := logIn(ctx, session.client, session.endpoint, session.settings.Login)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("renewing login at %s: the server answered %s", session.settings.Login.Path, response.Status)
	}
	header, err := credential(response, body, session.settings)
	if err != nil {
		return "", fmt.Errorf("the login response did not provide its configured credential")
	}
	expression := strings.TrimSuffix(strings.TrimPrefix(session.settings.ExpiresIn, "{"), "}")
	value, ok := link.Evaluate(expression, link.Exchange{ResponseBody: string(body)})
	seconds, numeric := value.(float64)
	if !ok || !numeric || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || math.Trunc(seconds) != seconds || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return "", fmt.Errorf("auth.expires-in returned no positive integer access lifetime")
	}
	lifetime := time.Duration(seconds * float64(time.Second))
	margin := min(time.Minute, lifetime/2)
	renewAt := started.Add(lifetime - margin)
	if !session.now().Before(renewAt) {
		return "", fmt.Errorf("login exchange consumed its safe access lifetime")
	}
	session.header, session.started, session.renewAt = header, started, renewAt
	return header, nil
}
