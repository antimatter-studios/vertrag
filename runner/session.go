package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type credentialUse struct {
	header   string
	renew    func(context.Context) (string, error)
	carriers Carriers
}

func trackCredential(transaction *Transaction, credential Credential) *Transaction {
	header := credentialFor(transaction.source, credential)
	if header != "" && credential.Renew != nil {
		transaction.credential = &credentialUse{header: header, renew: credential.Renew, carriers: sessionCarriers(header)}
	}
	return transaction
}

func sessionCarriers(line string) Carriers {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return Carriers{}
	}
	name = strings.TrimSpace(name)
	if !strings.EqualFold(name, "Cookie") {
		return Carriers{Headers: []string{name}}
	}
	var names []string
	for _, pair := range splitCookies(value) {
		names = append(names, cookieName(pair))
	}
	slices.Sort(names)
	return Carriers{Cookies: names}
}

func (transaction *Transaction) renewCredential(ctx context.Context) error {
	use := transaction.credential
	if use == nil {
		return nil
	}
	expected := map[string]string{}
	applyHeaderLine(expected, use.header)
	if use.carriers.collect(transaction.Request.Headers) != use.carriers.collect(expected) {
		return nil
	}
	header, err := use.renew(ctx)
	if err != nil {
		return fmt.Errorf("renewing the request's configured credential: %w", err)
	}
	carriers := sessionCarriers(header)
	if !slices.EqualFunc(carriers.Headers, use.carriers.Headers, strings.EqualFold) || !slices.Equal(carriers.Cookies, use.carriers.Cookies) || header == "" {
		return fmt.Errorf("renewing the configured credential changed its carrier")
	}
	applyHeaderLine(transaction.Request.Headers, header)
	use.header = header
	return nil
}
