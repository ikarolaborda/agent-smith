/*
RPC address auto-discovery. The worker's link-local (169.254.x) address rotates
on every reboot and the worker is multi-homed (a Thunderbolt bridge plus other
interfaces) while rpc-server binds exactly ONE interface. Hardcoding an IP goes
stale; trusting a multi-homed .local name lets the dialer pick an interface where
nothing is listening. So instead of pinning an address we resolve the configured
host to its candidate IPs and probe the RPC port, selecting the one that is
actually accepting connections right now — re-done every launch, immune to drift.
Under private_cluster_only the candidate set is filtered to private addresses and
resolution fails CLOSED (never a hostname fallback that could later resolve to a
public or wrong interface).
*/
package cluster

import (
	"context"
	"net"
	"sort"
	"time"
)

/* defaultRPCResolveTimeout bounds a single candidate probe dial. */
const defaultRPCResolveTimeout = 2 * time.Second

/*
resolveRPCAddr turns a configured rpc target (host:port — host may be a .local
name, an ephemeral link-local IP, or a literal) into a concrete ip:port that is
accepting connections now, or reports failure. privateOnly mirrors
runtime.private_cluster_only: when set, non-private candidates are dropped and a
miss fails closed instead of falling back to the unresolved host.
*/
func resolveRPCAddr(ctx context.Context, hostPort string, privateOnly bool, timeout time.Duration) (string, bool) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "", false
	}
	if timeout <= 0 {
		timeout = defaultRPCResolveTimeout
	}

	var candidates []string
	if ip := net.ParseIP(host); ip != nil {
		candidates = []string{host}
	} else {
		/*
			Bound the DNS/mDNS lookup explicitly: the package-level net.LookupHost
			ignores the caller's context, so a slow/blackholed resolver could stall
			Start. net.Resolver.LookupHost honors the deadline.
		*/
		lookupCtx, cancel := context.WithTimeout(ctx, timeout)
		addrs, err := net.DefaultResolver.LookupHost(lookupCtx, host)
		cancel()
		if err != nil {
			return "", false
		}
		candidates = addrs
	}

	candidates = orderRPCCandidates(filterPrivateCandidates(candidates, privateOnly))

	if addr, ok := selectReachable(ctx, candidates, port, timeout); ok {
		return addr, true
	}

	/*
		Nothing answered. Under private_cluster_only fail closed: handing back the
		raw hostPort would let llama-server resolve it to a different (possibly
		public or non-listening) interface than the probe vetted. When the policy
		is off, a permissive caller may still try the unresolved target.
	*/
	if privateOnly {
		return "", false
	}
	return hostPort, true
}

/*
filterPrivateCandidates drops any address that is not loopback/private/link-local
when privateOnly is set. RPC is unauthenticated, so a public dial target is never
acceptable under private_cluster_only — this is the security gate applied to the
RESOLVED ip, not just the configured hostname.
*/
func filterPrivateCandidates(ips []string, privateOnly bool) []string {
	if !privateOnly {
		return ips
	}
	out := make([]string, 0, len(ips))
	for _, a := range ips {
		ip := net.ParseIP(a)
		if ip == nil || isPublicIP(ip) {
			continue
		}
		out = append(out, a)
	}
	return out
}

/*
orderRPCCandidates dedups and orders candidates link-local-first, then sorts
within each group for determinism (resolver/DNS order varies by OS, which would
otherwise make interface selection flaky). Link-local first because the
Thunderbolt bridge uses 169.254.x and is the preferred high-bandwidth path.
*/
func orderRPCCandidates(ips []string) []string {
	seen := map[string]bool{}
	var linkLocal, rest []string
	for _, a := range ips {
		if seen[a] {
			continue
		}
		seen[a] = true
		ip := net.ParseIP(a)
		if ip != nil && ip.IsLinkLocalUnicast() {
			linkLocal = append(linkLocal, a)
			continue
		}
		rest = append(rest, a)
	}
	sort.Strings(linkLocal)
	sort.Strings(rest)
	return append(linkLocal, rest...)
}

/*
selectReachable probes each candidate's RPC port and returns the first ip:port
that accepts a TCP connection. A successful connect proves a listener is there,
not its identity — acceptable on a private unauthenticated cluster.
*/
func selectReachable(ctx context.Context, ips []string, port string, timeout time.Duration) (string, bool) {
	for _, ip := range ips {
		if ctx.Err() != nil {
			return "", false
		}
		addr := net.JoinHostPort(ip, port)
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			_ = conn.Close()
			return addr, true
		}
	}
	return "", false
}
