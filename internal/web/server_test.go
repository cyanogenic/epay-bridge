package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"epay-bridge/internal/config"
	"epay-bridge/internal/epay"
	"epay-bridge/internal/service"
)

func TestDisabledPaymentMethodIsRejectedBeforeOrderCreation(t *testing.T) {
	merchantKey := strings.Repeat("disabled-provider-key-", 2)
	cfg := config.Config{
		EPayPID: "newapi-main", EPayKey: merchantKey, MaxAmountCents: 10_000_000,
		AllowedOrigins: map[string]struct{}{"https://newapi.example.com": {}},
	}
	payments := service.NewPayments(cfg, nil)
	server, err := NewServer(cfg, nil, payments)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	form := url.Values{
		"pid": {cfg.EPayPID}, "type": {"wxpay"}, "out_trade_no": {"disabled_wxpay_1"},
		"notify_url": {"https://newapi.example.com/api/user/epay/notify"},
		"return_url": {"https://newapi.example.com/console"},
		"name":       {"Balance top-up"}, "money": {"0.01"}, "sign_type": {"MD5"},
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

	request := httptest.NewRequest(http.MethodPost, "/submit.php", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "payment method unavailable") {
		t.Fatalf("disabled method response: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestDisabledProviderCallbacksAreNotFound(t *testing.T) {
	cfg := config.Config{}
	payments := service.NewPayments(cfg, nil)
	server, err := NewServer(cfg, nil, payments)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	for _, path := range []string{"/callbacks/alipay", "/callbacks/wechat"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/returns/alipay", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("/returns/alipay status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
