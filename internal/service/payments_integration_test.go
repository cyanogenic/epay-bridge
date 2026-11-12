//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"epay-bridge/internal/config"
	"epay-bridge/internal/domain"
	"epay-bridge/internal/epay"
	"epay-bridge/internal/provider"
	"epay-bridge/internal/store"
)

func TestPaymentsPostgresLifecycle(t *testing.T) {
	databaseURL := os.Getenv("EPAY_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EPAY_INTEGRATION_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer database.Close()

	callbackForms := make(chan url.Values, 2)
	callback := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse New API callback: %v", err)
			http.Error(w, "failure", http.StatusBadRequest)
			return
		}
		form := make(url.Values, len(r.Form))
		for key, values := range r.Form {
			form[key] = append([]string(nil), values...)
		}
		callbackForms <- form
		_, _ = w.Write([]byte("success"))
	}))
	defer callback.Close()

	merchantKey := strings.Repeat("integration-key-", 3)
	cfg := config.Config{
		EPayPID: "integration-merchant", EPayKey: merchantKey,
		OrderTTL: 30 * time.Minute,
	}
	fake := &integrationProvider{name: "alipay", checkoutURL: "https://openapi.alipay.test/checkout"}
	payments := NewPayments(cfg, database, fake)
	payments.client = callback.Client()
	payments.client.Timeout = 5 * time.Second
	payments.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	merchantOrderNo := fmt.Sprintf("integration_%d", time.Now().UnixNano())
	purchase := &epay.PurchaseRequest{
		PID: cfg.EPayPID, Type: "alipay", MerchantOrderNo: merchantOrderNo,
		NotifyURL: callback.URL + "/notify", ReturnURL: callback.URL + "/return",
		Name: "Integration subscription", Money: "12.34", AmountCents: 1234,
	}
	created, kind, err := payments.Create(ctx, purchase)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if kind != provider.CheckoutRedirect || created.ProviderPayURL != fake.checkoutURL {
		t.Fatalf("unexpected checkout: kind=%q order=%+v", kind, created)
	}

	retried, _, err := payments.Create(ctx, purchase)
	if err != nil {
		t.Fatalf("retry payment creation: %v", err)
	}
	if retried.ID != created.ID || fake.createCalls() != 1 {
		t.Fatalf("create was not idempotent: first=%s retry=%s provider_calls=%d", created.ID, retried.ID, fake.createCalls())
	}

	conflict := *purchase
	conflict.AmountCents = 1235
	conflict.Money = "12.35"
	if _, _, err := payments.Create(ctx, &conflict); !errors.Is(err, store.ErrOrderConflict) {
		t.Fatalf("conflicting retry error = %v, want %v", err, store.ErrOrderConflict)
	}

	fake.setEvent(provider.Event{
		MerchantOrderNo: merchantOrderNo, ProviderTransactionID: "alipay-integration-transaction",
		AmountCents: purchase.AmountCents, State: provider.PaymentPaid,
		PaidAt: time.Now().UTC(), Payload: []byte(`{"source":"integration"}`),
	})
	request := httptest.NewRequest(http.MethodPost, "/callbacks/alipay", nil)
	paid, err := payments.ProcessNotify(ctx, "alipay", request)
	if err != nil {
		t.Fatalf("process provider notification: %v", err)
	}
	if paid.Status != domain.StatusPaid || paid.ProviderTransactionID != "alipay-integration-transaction" {
		t.Fatalf("order was not marked paid: %+v", paid)
	}
	if _, err := payments.ProcessNotify(ctx, "alipay", request); err != nil {
		t.Fatalf("duplicate provider notification was not idempotent: %v", err)
	}

	due, err := database.DueDeliveries(ctx, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due deliveries = %d, %v; want one", len(due), err)
	}
	payments.deliverDue(ctx)

	select {
	case form := <-callbackForms:
		flat := make(map[string]string, len(form))
		for key, values := range form {
			if len(values) == 1 {
				flat[key] = values[0]
			}
		}
		valid, verifyErr := epay.Verify(flat, merchantKey)
		if verifyErr != nil || !valid {
			t.Fatalf("New API callback signature invalid: valid=%v error=%v form=%v", valid, verifyErr, form)
		}
		if form.Get("trade_status") != "TRADE_SUCCESS" || form.Get("out_trade_no") != merchantOrderNo || form.Get("money") != "12.34" {
			t.Fatalf("unexpected New API callback: %v", form)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("New API callback was not delivered")
	}

	due, err = database.DueDeliveries(ctx, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("delivered callback remains due: count=%d error=%v", len(due), err)
	}

	returnURL, err := payments.ReturnURL(*paid)
	if err != nil {
		t.Fatalf("build return URL: %v", err)
	}
	parsedReturn, err := url.Parse(returnURL)
	if err != nil {
		t.Fatalf("parse return URL: %v", err)
	}
	returnValues := parsedReturn.Query()
	returnFlat := make(map[string]string, len(returnValues))
	for key, values := range returnValues {
		if len(values) == 1 {
			returnFlat[key] = values[0]
		}
	}
	valid, err := epay.Verify(returnFlat, merchantKey)
	if err != nil || !valid || returnValues.Get("trade_status") != "TRADE_SUCCESS" {
		t.Fatalf("invalid return URL: valid=%v error=%v url=%s", valid, err, returnURL)
	}
}

type integrationProvider struct {
	mu          sync.Mutex
	name        string
	checkoutURL string
	creates     int
	event       provider.Event
}

func (p *integrationProvider) Name() string { return p.name }

func (p *integrationProvider) Create(context.Context, domain.Order) (*provider.Checkout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	return &provider.Checkout{Kind: provider.CheckoutRedirect, URL: p.checkoutURL}, nil
}

func (p *integrationProvider) ParseNotify(*http.Request) (*provider.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	event := p.event
	return &event, nil
}

func (p *integrationProvider) Query(context.Context, domain.Order) (*provider.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	event := p.event
	return &event, nil
}

func (p *integrationProvider) Close(context.Context, domain.Order) error { return nil }

func (p *integrationProvider) createCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.creates
}

func (p *integrationProvider) setEvent(event provider.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.event = event
}
