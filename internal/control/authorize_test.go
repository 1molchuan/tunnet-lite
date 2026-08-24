package control

import "testing"

func TestVerificationURLFindsTheAccessHost(t *testing.T) {
	payload := []byte(`{"access":{"state":"pending",
		"verify":"https://access.rotating-root.example/#ticket123",
		"docs":"https://www.rotating-root.example/help"}}`)
	got := VerificationURL(payload)
	if got != "https://access.rotating-root.example/#ticket123" {
		t.Errorf("got %q", got)
	}
}

func TestVerificationURLIgnoresOtherHosts(t *testing.T) {
	payload := []byte(`{"docs":"https://www.example.com/help"}`)
	if got := VerificationURL(payload); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestOriginOfRejectsUnusableValues(t *testing.T) {
	for _, raw := range []string{"", "http://access.example/", "not a url", "https://"} {
		if _, err := originOf(raw); err == nil {
			t.Errorf("originOf(%q) should have failed", raw)
		}
	}
	got, err := originOf("https://access.example/#ticket")
	if err != nil || got != "https://access.example" {
		t.Errorf("got %q, %v", got, err)
	}
}
