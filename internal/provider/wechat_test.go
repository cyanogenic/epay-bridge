package provider

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	input := strings.Repeat("订阅套餐", 50)
	got := truncateUTF8(input, 127)
	if len(got) > 127 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncation: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if truncateUTF8("short", 127) != "short" {
		t.Fatal("short string changed")
	}
}
