package rag

import "testing"

/* TestTechnicalQuery checks the permissive gate fires on dev questions and skips chit-chat. */
func TestTechnicalQuery(t *testing.T) {
	technical := []string{
		"how do I use the next.js app router for static generation",
		"what is the recommended way to configure a postgres connection pool",
		"explain react useEffect cleanup with an example",
		"fix this build error in my docker compose file",
		"```go\nfor i := range xs {}\n```  why does this loop reuse the variable",
		"upgrade express from v4 to v5, what breaks",
	}
	for _, q := range technical {
		if !technicalQuery(q) {
			t.Errorf("technicalQuery(%q) = false, want true", q)
		}
	}

	chitChat := []string{
		"hi",
		"thanks!",
		"ok cool",
		"good morning",
		"how are you",
	}
	for _, q := range chitChat {
		if technicalQuery(q) {
			t.Errorf("technicalQuery(%q) = true, want false", q)
		}
	}
}
