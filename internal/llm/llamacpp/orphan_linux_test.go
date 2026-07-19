//go:build linux

package llamacpp

import "testing"

func TestParsePPIDFromStat(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want int
	}{
		{"simple", "1457918 (llama-server) S 1 1457918 ...", 1},
		{
			"comm with spaces and parens",
			"4242 (weird (proc) name) S 998 4242 4242 0 -1 ...",
			998,
		},
		{"reparented to init", "5000 (llama-server) S 1 5000 ...", 1},
		{"malformed", "not a stat line", 0},
		{"no fields after comm", "10 (x)", 0},
	}
	for _, c := range cases {
		if got := parsePPIDFromStat(c.stat); got != c.want {
			t.Errorf("%s: parsePPIDFromStat = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestReapSignatureIsScoped(t *testing.T) {
	// The signature must be our lock dir, never a bare "llama-server" match, so a
	// user's own server is never a candidate. Empty cache dir disables reaping.
	sig := ourRuntimeSignature()
	if sig == "" {
		t.Skip("no user cache dir in this environment")
	}
	if sig == "llama-server" || sig == "" {
		t.Fatalf("signature too broad: %q", sig)
	}
}
