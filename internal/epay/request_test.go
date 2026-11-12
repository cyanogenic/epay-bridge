package epay

import (
	"net/url"
	"testing"
)

func TestParseCents(t *testing.T) {
	tests := map[string]int64{"0.01": 1, "1": 100, "12.3": 1230, "12.34": 1234}
	for input, want := range tests {
		got, err := ParseCents(input)
		if err != nil || got != want {
			t.Fatalf("ParseCents(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "1.001", "1e2", ".1", "01.00"} {
		if _, err := ParseCents(input); err == nil {
			t.Fatalf("ParseCents(%q) succeeded", input)
		}
	}
}

func TestParsePurchase(t *testing.T) {
	values := url.Values{
		"pid": {"1001"}, "type": {"alipay"}, "out_trade_no": {"USR1NOabc"},
		"notify_url": {"https://api.example.com/api/user/epay/notify"},
		"return_url": {"https://api.example.com/usage-logs"},
		"name":       {"TUC1"}, "money": {"1.00"}, "device": {"pc"}, "sign_type": {"MD5"},
	}
	flat := map[string]string{}
	for k, v := range values {
		flat[k] = v[0]
	}
	sign, err := Sign(flat, "key")
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", sign)
	req, err := ParsePurchase(values, "1001", "key", 100000, map[string]struct{}{"https://api.example.com": {}})
	if err != nil {
		t.Fatal(err)
	}
	if req.AmountCents != 100 || req.MerchantOrderNo != "USR1NOabc" {
		t.Fatalf("unexpected request: %+v", req)
	}
}
