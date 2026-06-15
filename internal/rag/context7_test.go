package rag_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/context7"
)

/* stubContext7 returns canned docs so Augment plumbing can be tested without a live API. */
type stubContext7 struct {
	docs context7.Docs
	err  error
}

func (s stubContext7) LibraryDocs(_ context.Context, _ string) (context7.Docs, error) {
	if s.err != nil {
		return context7.Docs{}, s.err
	}
	return s.docs, nil
}

func TestAugment_RendersContext7Section(t *testing.T) {
	svc := newMemoryService(t)
	svc.Context7 = stubContext7{docs: context7.Docs{
		Library: "/vercel/next.js",
		Text:    "Use the app/ directory for the App Router.",
	}}

	aug := svc.Augment(context.Background(), "how do I use the next.js app router", "", false)
	if !strings.Contains(aug, "## Library documentation (Context7, authoritative)") {
		t.Fatalf("missing context7 section: %s", aug)
	}
	if !strings.Contains(aug, "/vercel/next.js") {
		t.Fatalf("missing library id: %s", aug)
	}
	if !strings.Contains(aug, "current, version-specific documentation fetched from Context7") {
		t.Fatalf("missing context7 behavior addendum: %s", aug)
	}
}

func TestAugment_SkipsContext7ForChitChat(t *testing.T) {
	svc := newMemoryService(t)
	svc.Context7 = stubContext7{docs: context7.Docs{Library: "/x/y", Text: "should not appear"}}

	aug := svc.Augment(context.Background(), "hi there", "", false)
	if strings.Contains(aug, "Context7") {
		t.Fatalf("context7 should not fire on chit-chat: %s", aug)
	}
}

func TestAugment_Context7FailureIsSilent(t *testing.T) {
	svc := newMemoryService(t)
	svc.Context7 = stubContext7{err: errors.New("network unreachable")}

	aug := svc.Augment(context.Background(), "how do I configure a postgres connection pool", "", false)
	if strings.Contains(aug, "Context7") {
		t.Fatalf("a context7 error must not surface in the prompt: %s", aug)
	}
}
