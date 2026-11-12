//go:build integration

package web

import (
	"bytes"
	"context"
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
	"epay-bridge/internal/service"
	"epay-bridge/internal/store"
)

func TestHTTPWeChatNativeCheckout(t *testing.T) {
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

	merchantKey := strings.Repeat("http-integration-key-", 2)
	cfg := config.Config{
		PublicBaseURL: "https://pay.example.com", EPayPID: "newapi-main", EPayKey: merchantKey,
		AllowedOrigins: map[string]struct{}{"https://newapi.example.com": {}},
		MaxAmountCents: 10_000_000, OrderTTL: 30 * time.Minute,
	}
	fake := &webIntegrationProvider{name: "wxpay", checkoutURL: "weixin://wxpay/bizpayurl?pr=integration"}
	payments := service.NewPayments(cfg, database, fake)
	server, err := NewServer(cfg, database, payments)
	if err != nil {
		t.Fatalf("create HTTP server: %v", err)
	}
	handler := server.Handler()

	merchantOrderNo := fmt.Sprintf("http_integration_%d", time.Now().UnixNano())
	form := url.Values{
		"pid": {cfg.EPayPID}, "type": {"wxpay"}, "out_trade_no": {merchantOrderNo},
		"notify_url": {"https://newapi.example.com/api/user/epay/notify"},
		"return_url": {"https://newapi.example.com/console"},
		"name":       {"Integration balance top-up"}, "money": {"9.99"},
		"device": {"pc"}, "sign_type": {"MD5"},
	}
	flat := make(map[string]string, len(form))
	for key, values := range form {
		flat[key] = values[0]
	}
	signature, err := epay.Sign(flat, merchantKey)
	if err != nil {
		t.Fatalf("sign request: %v", err)
	}
	form.Set("sign", signature)

	location := submitIntegrationForm(t, handler, form)
	if !strings.HasPrefix(location, "/checkout/") {
		t.Fatalf("submit location = %q, want checkout path", location)
	}
	if fake.createCalls() != 1 {
		t.Fatalf("provider create calls = %d, want one", fake.createCalls())
	}
	if retriedLocation := submitIntegrationForm(t, handler, form); retriedLocation != location || fake.createCalls() != 1 {
		t.Fatalf("retry was not idempotent: first=%q retry=%q provider_calls=%d", location, retriedLocation, fake.createCalls())
	}

	pageRequest := httptest.NewRequest(http.MethodGet, location, nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageResponse.Body.String(), "微信扫码支付") {
		t.Fatalf("checkout page: status=%d body=%q", pageResponse.Code, pageResponse.Body.String())
	}
	if got := pageResponse.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("missing checkout security policy: %q", got)
	}

	qrRequest := httptest.NewRequest(http.MethodGet, location+"/qr.png", nil)
	qrResponse := httptest.NewRecorder()
	handler.ServeHTTP(qrResponse, qrRequest)
	if qrResponse.Code != http.StatusOK || qrResponse.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("QR response: status=%d content_type=%q", qrResponse.Code, qrResponse.Header().Get("Content-Type"))
	}
	if !bytes.HasPrefix(qrResponse.Body.Bytes(), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatal("QR endpoint did not return a PNG")
	}

	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyResponse := httptest.NewRecorder()
	handler.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != http.StatusOK || strings.TrimSpace(readyResponse.Body.String()) != "ok" {
		t.Fatalf("ready response: status=%d body=%q", readyResponse.Code, readyResponse.Body.String())
	}

	invalid := cloneValues(form)
	invalid.Set("money", "10.00")
	invalidRequest := httptest.NewRequest(http.MethodPost, "/submit.php", strings.NewReader(invalid.Encode()))
	invalidRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("tampered request status = %d, want %d", invalidResponse.Code, http.StatusBadRequest)
	}
}

func submitIntegrationForm(t *testing.T, handler http.Handler, form url.Values) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/submit.php", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("submit status = %d, want %d; body=%q", response.Code, http.StatusSeeOther, response.Body.String())
	}
	return response.Header().Get("Location")
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

type webIntegrationProvider struct {
	mu          sync.Mutex
	name        string
	checkoutURL string
	creates     int
}

func (p *webIntegrationProvider) Name() string { return p.name }

func (p *webIntegrationProvider) Create(context.Context, domain.Order) (*provider.Checkout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	return &provider.Checkout{Kind: provider.CheckoutQRCode, URL: p.checkoutURL}, nil
}

func (p *webIntegrationProvider) ParseNotify(*http.Request) (*provider.Event, error) {
	return nil, fmt.Errorf("not used")
}

func (p *webIntegrationProvider) Query(context.Context, domain.Order) (*provider.Event, error) {
	return nil, fmt.Errorf("not used")
}

func (p *webIntegrationProvider) Close(context.Context, domain.Order) error { return nil }

func (p *webIntegrationProvider) createCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.creates
}
