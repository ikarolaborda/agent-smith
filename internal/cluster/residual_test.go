package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* R3: private_cluster_only DNS enforcement classifier. */
func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"93.184.216.34", true},
		{"10.0.0.5", false},
		{"192.168.1.10", false},
		{"172.16.4.2", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
		{"::1", false},
		{"fe80::1", false},
		{"fd00::1", false},
		{"0.0.0.0", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := isPublicIP(ip); got != c.public {
			t.Errorf("isPublicIP(%s) = %v, want %v", c.ip, got, c.public)
		}
	}
	if isPublicIP(nil) {
		t.Error("isPublicIP(nil) must be false")
	}
}

func TestHostResolvesPublic(t *testing.T) {
	/* Literal private/loopback IPs must never be flagged public (no DNS needed). */
	for _, h := range []string{"127.0.0.1", "192.168.0.2", "10.1.2.3", "::1"} {
		if hostResolvesPublic(h) {
			t.Errorf("hostResolvesPublic(%s) = true, want false", h)
		}
	}
	/* A literal public IP is flagged. */
	if !hostResolvesPublic("8.8.8.8") {
		t.Error("hostResolvesPublic(8.8.8.8) = false, want true")
	}
}

/* R2: restart backoff is exponential, capped, and jittered upward only. */
func TestRestartBackoff(t *testing.T) {
	for attempt, wantMin := range map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
	} {
		got := restartBackoff(attempt)
		if got < wantMin {
			t.Errorf("restartBackoff(%d) = %v, want >= %v", attempt, got, wantMin)
		}
	}
	/* Capped at 30s + <=25% jitter for large attempts. */
	if got := restartBackoff(50); got > 30*time.Second+30*time.Second/4 {
		t.Errorf("restartBackoff(50) = %v, exceeds cap+jitter", got)
	}
}

/* R2: Stop terminates the supervised process promptly (not after its natural run). */
func TestSupervisorStopKillsProcess(t *testing.T) {
	sup := newSupervisor(spawnSpec{
		name: "sleeper",
		path: "/bin/sleep",
		args: []string{"30"},
	}, "never", 0, discardLogger(), NewCollector())

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("Stop took %v; expected prompt kill, not waiting out the 30s sleep", elapsed)
	}
}

/*
R2 race: Stop during a pending restart backoff must suppress the relaunch. A
process that exits immediately would climb to max_restart_attempts; stopping it
while the first backoff is pending must freeze the restart count, proving no
relaunch races past Stop.
*/
func TestStopSuppressesPendingRestart(t *testing.T) {
	sup := newSupervisor(spawnSpec{
		name: "flapper",
		path: "/bin/sh",
		args: []string{"-c", "exit 1"},
	}, "on_failure", 3, discardLogger(), NewCollector())

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	/* Let the child exit and the watcher enter its first (~1s) backoff. */
	time.Sleep(200 * time.Millisecond)
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	/* Wait well past the first backoff window; no further restart may occur. */
	time.Sleep(2 * time.Second)
	if n := sup.RestartCount(); n > 1 {
		t.Fatalf("restart count = %d after Stop; pending restart was not suppressed", n)
	}
}

/* R1: a streaming request invokes exactly one backend — no post-commit replay. */
func TestNoFallbackReplayAfterStreamStart(t *testing.T) {
	cfg := testConfig()
	local := &fakeProvider{name: "ollama", tokens: []string{"a", "b", "c"}}
	p, err := New(context.Background(), cfg, local, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	ch, err := p.ChatStream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}
	if local.streamCalls != 1 {
		t.Fatalf("local provider invoked %d times; a streamed request must commit to exactly one backend", local.streamCalls)
	}
}
