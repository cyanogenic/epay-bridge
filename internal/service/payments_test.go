package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"
	"epay-bridge/internal/provider"
)

func TestRetryDelayIsCapped(t *testing.T) {
	if got := retryDelay(1); got != 5*time.Second {
		t.Fatalf("first delay = %v", got)
	}
	if got := retryDelay(100); got != 6*time.Hour {
		t.Fatalf("capped delay = %v", got)
	}
}

func TestSupportsOnlyRegisteredProviders(t *testing.T) {
	payments := NewPayments(config.Config{}, nil, namedProvider{name: "alipay"})
	if !payments.Supports("alipay") {
		t.Fatal("registered provider is not supported")
	}
	if payments.Supports("wxpay") {
		t.Fatal("unregistered provider is supported")
	}
}

type namedProvider struct{ name string }

func (p namedProvider) Name() string { return p.name }
func (namedProvider) Create(context.Context, domain.Order) (*provider.Checkout, error) {
	return nil, nil
}
func (namedProvider) ParseNotify(*http.Request) (*provider.Event, error) { return nil, nil }
func (namedProvider) Query(context.Context, domain.Order) (*provider.Event, error) {
	return nil, nil
}
func (namedProvider) Close(context.Context, domain.Order) error { return nil }
