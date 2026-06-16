package cluster

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestFilterPrivateCandidates(t *testing.T) {
	t.Run("it_drops_public_addresses_when_private_only", func(t *testing.T) {
		got := filterPrivateCandidates([]string{"8.8.8.8", "169.254.1.2", "192.168.0.5", "1.1.1.1"}, true)
		want := []string{"169.254.1.2", "192.168.0.5"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("filtered = %v, want %v", got, want)
		}
	})

	t.Run("it_passes_everything_through_when_policy_is_off", func(t *testing.T) {
		in := []string{"8.8.8.8", "169.254.1.2"}
		if got := filterPrivateCandidates(in, false); !reflect.DeepEqual(got, in) {
			t.Fatalf("filtered = %v, want %v", got, in)
		}
	})
}

func TestOrderRPCCandidates(t *testing.T) {
	t.Run("it_puts_link_local_first_dedups_and_sorts", func(t *testing.T) {
		got := orderRPCCandidates([]string{"192.168.0.9", "169.254.9.9", "192.168.0.9", "169.254.1.1"})
		want := []string{"169.254.1.1", "169.254.9.9", "192.168.0.9"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ordered = %v, want %v", got, want)
		}
	})
}

func TestSelectReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	t.Run("it_skips_a_dead_candidate_and_returns_the_live_one", func(t *testing.T) {
		/* 127.0.0.2 refuses instantly; 127.0.0.1 has the listener. */
		got, ok := selectReachable(context.Background(), []string{"127.0.0.2", "127.0.0.1"}, port, 500*time.Millisecond)
		if !ok || got != net.JoinHostPort("127.0.0.1", port) {
			t.Fatalf("selected %q ok=%v, want the live 127.0.0.1 listener", got, ok)
		}
	})

	t.Run("it_reports_failure_when_none_accept", func(t *testing.T) {
		if _, ok := selectReachable(context.Background(), []string{"127.0.0.2"}, "1", 300*time.Millisecond); ok {
			t.Fatal("expected no reachable candidate")
		}
	})
}

func TestResolveRPCAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	t.Run("it_returns_a_reachable_literal_private_ip", func(t *testing.T) {
		addr, ok := resolveRPCAddr(context.Background(), net.JoinHostPort("127.0.0.1", port), true, 500*time.Millisecond)
		if !ok || addr != net.JoinHostPort("127.0.0.1", port) {
			t.Fatalf("resolved %q ok=%v, want the loopback listener", addr, ok)
		}
	})

	t.Run("it_fails_closed_on_a_public_literal_under_private_only", func(t *testing.T) {
		/* Must not even dial: dropped by the private filter, then fail-closed. */
		if addr, ok := resolveRPCAddr(context.Background(), "8.8.8.8:50052", true, 300*time.Millisecond); ok {
			t.Fatalf("resolved %q, want fail-closed for a public target under private_cluster_only", addr)
		}
	})

	t.Run("it_fails_closed_when_nothing_listens_under_private_only", func(t *testing.T) {
		if addr, ok := resolveRPCAddr(context.Background(), "127.0.0.2:1", true, 300*time.Millisecond); ok {
			t.Fatalf("resolved %q, want fail-closed when no candidate accepts", addr)
		}
	})

	t.Run("it_falls_back_to_the_unresolved_target_when_policy_is_off", func(t *testing.T) {
		addr, ok := resolveRPCAddr(context.Background(), "127.0.0.2:1", false, 300*time.Millisecond)
		if !ok || addr != "127.0.0.2:1" {
			t.Fatalf("resolved %q ok=%v, want permissive fallback to the configured target", addr, ok)
		}
	})
}

/*
An unreachable configured worker must make resolveRPCHosts ERROR, so the llama
backend refuses to launch single-node (which would risk OOMing the coordinator)
and the scheduler falls back to local instead.
*/
func TestResolveRPCHostsErrorsOnUnreachableWorker(t *testing.T) {
	rt := RuntimeConfig{PrivateClusterOnly: true}
	rt.Llama.RPCHosts = []string{"127.0.0.2:1"}
	rt.Llama.RPCPort = 50052
	b := newLlamaBackend(rt, discardLogger(), NewCollector())

	if _, err := b.resolveRPCHosts(context.Background(), nil); err == nil {
		t.Fatal("expected an error when the configured rpc host is unreachable under private_cluster_only")
	}
}
