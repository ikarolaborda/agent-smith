package rag

import "testing"

func TestRecallAt(t *testing.T) {
	cases := []struct {
		name      string
		retrieved map[string]bool
		relevant  []string
		want      float64
	}{
		{"half", map[string]bool{"a": true, "b": true}, []string{"a", "c"}, 0.5},
		{"full", map[string]bool{"a": true}, []string{"a"}, 1.0},
		{"none", nil, []string{"a", "b"}, 0.0},
		{"empty-relevant-is-perfect", map[string]bool{}, nil, 1.0},
	}
	for _, c := range cases {
		if got := recallAt(c.retrieved, c.relevant); got != c.want {
			t.Errorf("%s: recallAt = %v, want %v", c.name, got, c.want)
		}
	}
}
