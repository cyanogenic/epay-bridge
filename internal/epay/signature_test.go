package epay

import "testing"

func TestSignAndVerify(t *testing.T) {
	params := map[string]string{
		"pid":          "1001",
		"type":         "wxpay",
		"out_trade_no": "USR1NOabc",
		"money":        "0.01",
		"name":         "TUC1",
		"sign_type":    "MD5",
	}
	signed, err := SignedCopy(params, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(signed, "secret")
	if err != nil || !ok {
		t.Fatalf("Verify() = %v, %v", ok, err)
	}
	signed["money"] = "100.00"
	ok, err = Verify(signed, "secret")
	if err != nil || ok {
		t.Fatalf("tampered Verify() = %v, %v", ok, err)
	}
}

func TestRejectsAmbiguousValues(t *testing.T) {
	_, err := Sign(map[string]string{"return_url": "https://example.com/a?x=1&y=2"}, "key")
	if err == nil {
		t.Fatal("expected ambiguous value to be rejected")
	}
}
