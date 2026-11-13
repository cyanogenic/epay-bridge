package config

import (
	"strings"
	"testing"
)

func TestParseOrigins(t *testing.T) {
	origins, err := parseOrigins("https://api.example.com, https://console.example.com:8443")
	if err != nil {
		t.Fatal(err)
	}
	if len(origins) != 2 {
		t.Fatalf("got %d origins", len(origins))
	}
	for _, input := range []string{"", "http://api.example.com", "https://api.example.com/path", "https://user@example.com"} {
		if _, err := parseOrigins(input); err == nil {
			t.Fatalf("parseOrigins(%q) succeeded", input)
		}
	}
}

func TestValidateProvidersRequiresAtLeastOne(t *testing.T) {
	err := (Config{}).validateProviders()
	if err == nil || !strings.Contains(err.Error(), "at least one payment provider") {
		t.Fatalf("validateProviders() error = %v", err)
	}
}

func TestValidateProvidersAllowsAlipayOnly(t *testing.T) {
	cfg := Config{Alipay: Alipay{
		Enabled: true, AppID: "2021000000000000", PrivateKeyFile: "/run/secrets/alipay/private.pem",
		AppPublicCertFile: "/run/secrets/alipay/app.crt", RootCertFile: "/run/secrets/alipay/root.crt",
		PublicCertFile: "/run/secrets/alipay/public.crt",
	}}
	if err := cfg.validateProviders(); err != nil {
		t.Fatalf("validateProviders() error = %v", err)
	}
}

func TestValidateProvidersAllowsWeChatOnly(t *testing.T) {
	cfg := Config{WeChat: WeChat{
		Enabled: true, AppID: "wx1234567890abcdef", MerchantID: "1900000001",
		SerialNumber: "0123456789ABCDEF", APIv3Key: strings.Repeat("k", 32),
		PrivateKeyFile: "/run/secrets/wechat/private.pem", PublicKeyFile: "/run/secrets/wechat/public.pem",
		PublicKeyID: "PUB_KEY_ID_0123456789ABCDEF",
	}}
	if err := cfg.validateProviders(); err != nil {
		t.Fatalf("validateProviders() error = %v", err)
	}
}

func TestValidateProvidersRejectsIncompleteEnabledProvider(t *testing.T) {
	cfg := Config{WeChat: WeChat{Enabled: true, MerchantID: "1900000001"}}
	if err := cfg.validateProviders(); err == nil || !strings.Contains(err.Error(), "WeChat Native is enabled") {
		t.Fatalf("validateProviders() error = %v", err)
	}
}

func TestLoadEnablesProviderFromSentinelFields(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ALIPAY_APP_ID", "2021000000000000")
	t.Setenv("ALIPAY_PRIVATE_KEY_FILE", "/run/secrets/alipay/private.pem")
	t.Setenv("ALIPAY_APP_PUBLIC_CERT_FILE", "/run/secrets/alipay/app.crt")
	t.Setenv("ALIPAY_ROOT_CERT_FILE", "/run/secrets/alipay/root.crt")
	t.Setenv("ALIPAY_PUBLIC_CERT_FILE", "/run/secrets/alipay/public.crt")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Alipay.Enabled || cfg.WeChat.Enabled {
		t.Fatalf("provider states: Alipay=%t WeChat=%t", cfg.Alipay.Enabled, cfg.WeChat.Enabled)
	}
}

func TestLoadEnablesWeChatFromMerchantID(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("WECHAT_APP_ID", "wx1234567890abcdef")
	t.Setenv("WECHAT_MERCHANT_ID", "1900000001")
	t.Setenv("WECHAT_SERIAL_NUMBER", "0123456789ABCDEF")
	t.Setenv("WECHAT_API_V3_KEY", strings.Repeat("k", 32))
	t.Setenv("WECHAT_PRIVATE_KEY_FILE", "/run/secrets/wechat/private.pem")
	t.Setenv("WECHAT_PUBLIC_KEY_FILE", "/run/secrets/wechat/public.pem")
	t.Setenv("WECHAT_PUBLIC_KEY_ID", "PUB_KEY_ID_0123456789ABCDEF")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Alipay.Enabled || !cfg.WeChat.Enabled {
		t.Fatalf("provider states: Alipay=%t WeChat=%t", cfg.Alipay.Enabled, cfg.WeChat.Enabled)
	}
}

func TestLoadRejectsNoProviders(t *testing.T) {
	setRequiredEnvironment(t)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "at least one payment provider") {
		t.Fatalf("Load() error = %v", err)
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"PUBLIC_BASE_URL":             "https://pay.example.com",
		"DATABASE_URL":                "postgres://epay:password@postgres:5432/epay",
		"EPAY_PID":                    "newapi-main",
		"EPAY_KEY":                    strings.Repeat("e", 32),
		"NEW_API_ALLOWED_ORIGINS":     "https://newapi.example.com",
		"ALIPAY_APP_ID":               "",
		"ALIPAY_PRIVATE_KEY_FILE":     "",
		"ALIPAY_APP_PUBLIC_CERT_FILE": "",
		"ALIPAY_ROOT_CERT_FILE":       "",
		"ALIPAY_PUBLIC_CERT_FILE":     "",
		"ALIPAY_PUBLIC_KEY_FILE":      "",
		"WECHAT_APP_ID":               "",
		"WECHAT_MERCHANT_ID":          "",
		"WECHAT_SERIAL_NUMBER":        "",
		"WECHAT_API_V3_KEY":           "",
		"WECHAT_PRIVATE_KEY_FILE":     "",
		"WECHAT_PUBLIC_KEY_FILE":      "",
		"WECHAT_PUBLIC_KEY_ID":        "",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}
