package db

import "testing"

func TestNormaliseURLPathRejectsEmptyHost(t *testing.T) {
	_, _, err := normaliseURLPath("/relative", "")
	if err == nil {
		t.Fatal("expected error when host and fallback domain are empty")
	}
}

func TestNormaliseURLPathStripsWWW(t *testing.T) {
	host, path, err := normaliseURLPath("https://www.example.com/hover", "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "example.com" {
		t.Errorf("expected host %q, got %q", "example.com", host)
	}
	if path != "/hover" {
		t.Errorf("expected path %q, got %q", "/hover", path)
	}
}

func TestNormaliseURLPathRelativeURLWithWWWFallbackDomain(t *testing.T) {
	host, path, err := normaliseURLPath("/hover", "www.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "example.com" {
		t.Errorf("expected host %q, got %q", "example.com", host)
	}
	if path != "/hover" {
		t.Errorf("expected path %q, got %q", "/hover", path)
	}
}
