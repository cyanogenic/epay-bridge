package config

import "testing"

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
