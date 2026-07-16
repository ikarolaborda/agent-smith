package config

import (
	"os"
	"testing"
)

func TestExpandEnvRefs_PreservesLiteralsAndEscapes(t *testing.T) {
	t.Setenv("AS_TEST_TOKEN", "secret")

	cases := map[string]string{
		"budget of $100 and $5":  "budget of $100 and $5",
		"key=${AS_TEST_TOKEN}":   "key=secret",
		"key=$AS_TEST_TOKEN!":    "key=secret!",
		"price $$5 literal":      "price $5 literal",
		"missing=${AS_UNSET_XX}": "missing=",
		"bare $AS_UNSET_XX end":  "bare  end",
	}
	for in, want := range cases {
		if got := expandEnvRefs(in); got != want {
			t.Errorf("expandEnvRefs(%q) = %q, want %q", in, got, want)
		}
	}

	if _, ok := os.LookupEnv("AS_UNSET_XX"); ok {
		t.Fatal("test precondition: AS_UNSET_XX must be unset")
	}
}
