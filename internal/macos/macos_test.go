package macos

import "testing"

func TestParseMajorAcceptsRealVersions(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"26.6.2", 26},
		{"15.5", 15},
		{"26.0", 26},
		{"26", 26},
		{" 26.6.2\n", 26},
	} {
		got, err := parseMajor(tc.in)
		if err != nil {
			t.Fatalf("parseMajor(%q) errored: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseMajor(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// A version saddle cannot read must never be treated as passing the floor:
// silently running on an unverified OS is the failure this check exists to
// prevent.
func TestParseMajorRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "   ", "Tahoe", "v26.6", "-1", "0", "x.6.2"} {
		if got, err := parseMajor(in); err == nil {
			t.Fatalf("parseMajor(%q) = %d, want an error", in, got)
		}
	}
}

func TestFloorSeparatesSupportedFromUnsupported(t *testing.T) {
	below, err := parseMajor("15.5")
	if err != nil {
		t.Fatal(err)
	}
	if below >= Floor {
		t.Fatalf("macOS 15.5 must be below the floor of %d", Floor)
	}
	at, err := parseMajor("26.0")
	if err != nil {
		t.Fatal(err)
	}
	if at < Floor {
		t.Fatalf("macOS 26.0 must meet the floor of %d", Floor)
	}
}
