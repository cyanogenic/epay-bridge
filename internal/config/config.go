package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress  string
	PublicBaseURL  string
	DatabaseURL    string
	EPayPID        string
	EPayKey        string
	AllowedOrigins map[string]struct{}
	MaxAmountCents int64
	OrderTTL       time.Duration
	AdminUsername  string
	AdminPassword  string
	Alipay         Alipay
	WeChat         WeChat
}

type Alipay struct {
	Enabled           bool
	AppID             string
	PrivateKeyFile    string
	AppPublicCertFile string
	RootCertFile      string
	PublicCertFile    string
	PublicKeyFile     string
	Production        bool
}

type WeChat struct {
	Enabled        bool
	AppID          string
	MerchantID     string
	SerialNumber   string
	APIv3Key       string
	PrivateKeyFile string
	PublicKeyFile  string
	PublicKeyID    string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:  env("LISTEN_ADDRESS", ":8080"),
		PublicBaseURL:  strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		EPayPID:        os.Getenv("EPAY_PID"),
		EPayKey:        os.Getenv("EPAY_KEY"),
		MaxAmountCents: envInt64("MAX_ORDER_CENTS", 10_000_000),
		OrderTTL:       envDuration("ORDER_TTL", 30*time.Minute),
		AdminUsername:  os.Getenv("ADMIN_USERNAME"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),
		Alipay: Alipay{
			AppID:             os.Getenv("ALIPAY_APP_ID"),
			PrivateKeyFile:    os.Getenv("ALIPAY_PRIVATE_KEY_FILE"),
			AppPublicCertFile: os.Getenv("ALIPAY_APP_PUBLIC_CERT_FILE"),
			RootCertFile:      os.Getenv("ALIPAY_ROOT_CERT_FILE"),
			PublicCertFile:    os.Getenv("ALIPAY_PUBLIC_CERT_FILE"),
			PublicKeyFile:     os.Getenv("ALIPAY_PUBLIC_KEY_FILE"),
			Production:        envBool("ALIPAY_PRODUCTION", true),
		},
		WeChat: WeChat{
			AppID:          os.Getenv("WECHAT_APP_ID"),
			MerchantID:     os.Getenv("WECHAT_MERCHANT_ID"),
			SerialNumber:   os.Getenv("WECHAT_SERIAL_NUMBER"),
			APIv3Key:       os.Getenv("WECHAT_API_V3_KEY"),
			PrivateKeyFile: os.Getenv("WECHAT_PRIVATE_KEY_FILE"),
			PublicKeyFile:  os.Getenv("WECHAT_PUBLIC_KEY_FILE"),
			PublicKeyID:    os.Getenv("WECHAT_PUBLIC_KEY_ID"),
		},
	}

	var err error
	cfg.AllowedOrigins, err = parseOrigins(os.Getenv("NEW_API_ALLOWED_ORIGINS"))
	if err != nil {
		return Config{}, err
	}
	if cfg.PublicBaseURL == "" || cfg.DatabaseURL == "" || cfg.EPayPID == "" || cfg.EPayKey == "" {
		return Config{}, errors.New("PUBLIC_BASE_URL, DATABASE_URL, EPAY_PID and EPAY_KEY are required")
	}
	base, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return Config{}, errors.New("PUBLIC_BASE_URL must be an HTTPS origin without a path")
	}
	if len(cfg.EPayKey) < 32 {
		return Config{}, errors.New("EPAY_KEY must contain at least 32 characters")
	}
	if len(cfg.EPayPID) > 64 || strings.TrimSpace(cfg.EPayPID) != cfg.EPayPID || strings.ContainsAny(cfg.EPayPID, "&=") {
		return Config{}, errors.New("EPAY_PID contains unsupported characters")
	}
	if cfg.MaxAmountCents < 1 || cfg.OrderTTL < time.Minute {
		return Config{}, errors.New("MAX_ORDER_CENTS or ORDER_TTL is invalid")
	}
	if (cfg.AdminUsername == "") != (cfg.AdminPassword == "") {
		return Config{}, errors.New("ADMIN_USERNAME and ADMIN_PASSWORD must either both be set or both be empty")
	}
	if cfg.AdminPassword != "" && len(cfg.AdminPassword) < 16 {
		return Config{}, errors.New("ADMIN_PASSWORD must contain at least 16 characters")
	}
	cfg.Alipay.Enabled = cfg.Alipay.AppID != ""
	cfg.WeChat.Enabled = cfg.WeChat.MerchantID != ""
	if err := cfg.validateProviders(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validateProviders() error {
	if !c.Alipay.Enabled && !c.WeChat.Enabled {
		return errors.New("at least one payment provider must be configured: set ALIPAY_APP_ID or WECHAT_MERCHANT_ID")
	}
	if c.Alipay.Enabled {
		if c.Alipay.PrivateKeyFile == "" {
			return errors.New("Alipay is enabled: ALIPAY_PRIVATE_KEY_FILE must be set")
		}
		certMode := c.Alipay.AppPublicCertFile != "" && c.Alipay.RootCertFile != "" && c.Alipay.PublicCertFile != ""
		publicKeyMode := c.Alipay.PublicKeyFile != ""
		if !certMode && !publicKeyMode {
			return errors.New("Alipay is enabled: either all three certificate files or ALIPAY_PUBLIC_KEY_FILE must be set")
		}
	}
	if c.WeChat.Enabled {
		if c.WeChat.AppID == "" || c.WeChat.SerialNumber == "" || c.WeChat.APIv3Key == "" || c.WeChat.PrivateKeyFile == "" || c.WeChat.PublicKeyFile == "" || c.WeChat.PublicKeyID == "" {
			return errors.New("WeChat Native is enabled: app ID, serial number, API v3 key, private key, payment public key and public key ID must be set")
		}
		if len(c.WeChat.APIv3Key) != 32 {
			return errors.New("WECHAT_API_V3_KEY must be exactly 32 characters")
		}
	}
	return nil
}

func parseOrigins(raw string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid New API allowed origin %q", value)
		}
		out[strings.ToLower(u.Scheme+"://"+u.Host)] = struct{}{}
	}
	if len(out) == 0 {
		return nil, errors.New("NEW_API_ALLOWED_ORIGINS must contain at least one HTTPS origin")
	}
	return out, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) int64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
