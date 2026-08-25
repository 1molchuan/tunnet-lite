package control

import "testing"

func TestMinimumVersionReadsTheAdvertisedFloor(t *testing.T) {
	// The response a client below the floor receives: a release block and no
	// runtime section at all.
	payload := []byte(`{"schema_version":2,"access":{"state":"ready"},
		"release":{"platform":"windows","latest_version":"0.2.5","minimum_version":"0.2.5"}}`)
	if got := MinimumVersion(payload); got != "0.2.5" {
		t.Errorf("got %q, want 0.2.5", got)
	}
}

func TestMinimumVersionIsEmptyWhenAbsent(t *testing.T) {
	for _, payload := range []string{`{"runtime":{}}`, `not json`, `{}`} {
		if got := MinimumVersion([]byte(payload)); got != "" {
			t.Errorf("%s: got %q, want empty", payload, got)
		}
	}
}

func TestVersionLessComparesComponentwise(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.4", "0.2.5", true},
		{"0.2.5", "0.2.5", false},
		{"0.2.5", "0.2.4", false},
		{"0.2.9", "0.3.0", true},
		{"0.10.0", "0.9.0", false}, // numeric, not lexicographic
		{"1.0", "1.0.1", true},     // missing components count as zero
		{"1.0.0", "1.0", false},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A version that cannot be parsed must not be treated as newer, or a garbled
// floor would silently stop the client from adopting a real one later.
func TestAMalformedVersionIsNotTreatedAsNewer(t *testing.T) {
	if versionLess("0.2.5", "not.a.version") {
		t.Error("a malformed floor should not appear newer than a real version")
	}
}
