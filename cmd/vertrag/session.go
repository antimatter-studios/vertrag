package main

import (
	"context"

	"github.com/antimatter-studios/vertrag/reporter"
)

func registeredRenewal(renew func(context.Context) (string, error)) func(context.Context) (string, error) {
	if renew == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		header, err := renew(ctx)
		if err != nil {
			return "", err
		}
		reporter.RegisterCredential(header)
		return header, nil
	}
}
