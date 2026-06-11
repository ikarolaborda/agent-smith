package server

import (
	"encoding/json"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* a 1x1 transparent PNG, base64 (no data: prefix). */
const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func decodeInbound(t *testing.T, body string) llm.Message {
	t.Helper()
	var im inboundMessage
	if err := json.Unmarshal([]byte(body), &im); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m, err := im.toMessage()
	if err != nil {
		t.Fatalf("toMessage: %v", err)
	}
	return m
}

func TestInboundMessage_PlainStringContentUnchanged(t *testing.T) {
	m := decodeInbound(t, `{"role":"user","content":"hello there"}`)
	if m.Role != llm.RoleUser || m.Content != "hello there" {
		t.Fatalf("got role=%q content=%q", m.Role, m.Content)
	}
	if len(m.Images) != 0 {
		t.Fatalf("expected no images, got %d", len(m.Images))
	}
}

func TestInboundMessage_MultimodalArrayExtractsTextAndImage(t *testing.T) {
	body := `{"role":"user","content":[` +
		`{"type":"text","text":"describe this"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGB64 + `"}}` +
		`]}`
	m := decodeInbound(t, body)
	if m.Content != "describe this" {
		t.Fatalf("text part not extracted: %q", m.Content)
	}
	if len(m.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(m.Images))
	}
	if m.Images[0].MimeType != "image/png" {
		t.Fatalf("mime: %q", m.Images[0].MimeType)
	}
	if m.Images[0].Data != tinyPNGB64 {
		t.Fatalf("image data mismatch")
	}
}

func TestInboundMessage_BareStringImageURL(t *testing.T) {
	body := `{"role":"user","content":[{"type":"image_url","image_url":"data:image/jpeg;base64,` + tinyPNGB64 + `"}]}`
	m := decodeInbound(t, body)
	if len(m.Images) != 1 || m.Images[0].MimeType != "image/jpeg" {
		t.Fatalf("bare-string image_url not handled: %+v", m.Images)
	}
}

func TestInboundMessage_NonDataURLRejected(t *testing.T) {
	body := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}`
	var im inboundMessage
	if err := json.Unmarshal([]byte(body), &im); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := im.toMessage(); err == nil {
		t.Fatal("expected error for non-data image URL, got nil")
	}
}

func TestCloudModelSupportsVision(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"anthropic", "claude-sonnet-4-5", true},
		{"openai", "gpt-4o-mini", true},
		{"openai", "gpt-3.5-turbo", false},
		{"ollama", "anything", false},
	}
	for _, c := range cases {
		if got := cloudModelSupportsVision(c.provider, c.model); got != c.want {
			t.Errorf("%s/%s: got %v want %v", c.provider, c.model, got, c.want)
		}
	}
}
