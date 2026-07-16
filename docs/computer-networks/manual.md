# Computer Networks — Engineering and Security Reference

This corpus is a compact operational reference, not a substitute for the cited
standards. Prefer the current RFC or vendor documentation when an exact field,
timer, default, or version matters.

Primary references: IETF RFC 8200 (IPv6), RFC 9293 (TCP), RFC 768 (UDP), RFC
9000 (QUIC), RFC 8446 (TLS 1.3), RFC 9110/9112/9113/9114 (HTTP), RFC 1034/1035
(DNS), IEEE 802.1Q (VLANs), NIST SP 800-41 (firewalls), and the Wireshark user
guide.

## Layering and Encapsulation

Use the TCP/IP model for engineering and the OSI model as diagnostic vocabulary:

1. Link: Ethernet, Wi-Fi, VLANs, ARP/Neighbor Discovery, frames, MAC addresses.
2. Internet: IPv4/IPv6, ICMP, routing, packets, IP addresses.
3. Transport: TCP, UDP, QUIC, ports, flows, congestion and reliability.
4. Application: DNS, HTTP, TLS, SSH, SMTP, database and service protocols.

Encapsulation adds a header at each layer. An HTTP request may be carried in a
TLS record, in a TCP segment, in an IP packet, in an Ethernet frame. The reverse
operation happens at the receiver. MTU applies at the link boundary; exceeding
it can cause IPv4 fragmentation or require IPv6 Path MTU Discovery. Treat a
black-holed ICMP "packet too big" message as a likely cause of connections that
establish but stall on larger payloads.

Do not infer security from a layer name. TLS authenticates and protects an
application stream; it does not make DNS, routing, endpoint authorization, or
the application itself trustworthy.

## Ethernet, Switching, VLANs, and ARP

- A switch learns source MAC addresses per port and forwards known unicast
  frames to the learned port. Unknown unicast, broadcast, and some multicast are
  flooded within the broadcast domain.
- A VLAN is a Layer-2 broadcast-domain boundary. IEEE 802.1Q tags carry a VLAN
  identifier over a trunk; access ports normally present one untagged VLAN to an
  endpoint.
- Spanning Tree prevents Layer-2 loops. A loop can create a broadcast storm and
  MAC-table instability fast enough to take down the segment.
- ARP maps IPv4 addresses to link-layer addresses. IPv6 uses ICMPv6 Neighbor
  Discovery. Neither classic ARP nor unauthenticated Neighbor Discovery proves
  endpoint identity.

Security posture: put user, server, management, build, and laboratory traffic in
separate segments; route through explicit policy; disable unused switch ports;
use DHCP snooping/dynamic ARP inspection where supported; and never treat VLAN
separation alone as authorization. Test VLAN-hopping and ARP-spoofing defenses
only on networks you own or are authorized to assess.

## IPv4, IPv6, Subnets, and NAT

An IP prefix identifies a network, not a host. In IPv4, `/24` means the first 24
bits are the network prefix and leaves 8 host bits. In IPv6, `/64` is the normal
LAN prefix. Longest-prefix match wins in a routing table.

Important IPv4 ranges include loopback `127.0.0.0/8`, link-local
`169.254.0.0/16`, and RFC 1918 private space `10.0.0.0/8`, `172.16.0.0/12`, and
`192.168.0.0/16`. IPv6 loopback is `::1`; link-local addresses are `fe80::/10`;
Unique Local Addresses are `fc00::/7` (commonly `fd00::/8`). Private or
link-local addressing limits routing scope but is not an authentication control.

NAT rewrites addresses and often ports. It conserves IPv4 space and creates
state, but it is not the same thing as a firewall. A stateful firewall makes an
explicit policy decision; NAT merely makes unsolicited inbound reachability less
convenient in common deployments. IPv6 normally restores end-to-end addressing,
so enforce inbound policy rather than relying on address translation.

## Routing

- Static routes are predictable and operationally simple for small topologies.
- OSPF and IS-IS are link-state interior gateway protocols: routers flood
  topology information and compute shortest paths.
- BGP exchanges reachability between autonomous systems and inside large
  networks. Policy, not raw shortest distance, drives selection.
- Equal-Cost Multi-Path can hash flows across several next hops. Packet-level
  spraying can reorder TCP; flow-level hashing usually avoids that.

When diagnosing a path, distinguish the control plane (what routes should
exist), data plane (what forwarding actually does), and management plane (how
devices are configured). Verify both directions: asymmetric routing can break a
stateful firewall even when the forward path looks correct.

## TCP

TCP provides an ordered reliable byte stream, not messages. Applications must
frame their own messages and must handle partial reads and writes.

Connection establishment uses SYN, SYN-ACK, ACK. Sequence numbers identify byte
positions. Receivers acknowledge progress and advertise a receive window (flow
control). Senders also maintain a congestion window (network congestion
control); the effective flight size is bounded by both.

Key behavior:

- Retransmission handles loss; duplicate acknowledgements and selective
  acknowledgements can trigger recovery before a timer expires.
- Slow start grows the congestion window quickly from a conservative initial
  value; congestion avoidance grows more cautiously.
- FIN performs an orderly half-close. RST aborts a connection. `TIME_WAIT`
  protects later connections from delayed old segments and is normal on the
  endpoint that actively closes.
- Keepalive is optional failure detection, not application health. Use bounded
  application deadlines and idempotent retry rules.
- A successful TCP connect proves that something accepted the connection at
  that address; it does not authenticate the service. TLS or another
  authenticated protocol must do that.

Common failures: SYN without SYN-ACK (routing/firewall/listener), handshake then
immediate RST (service/policy mismatch), small transfers work but large ones hang
(MTU), rising retransmissions (loss/congestion), many `TIME_WAIT` sockets
(connection churn), and exhausted ephemeral ports (too much churn or leaked
connections).

## UDP and QUIC

UDP preserves datagram boundaries but supplies no connection, delivery,
ordering, retransmission, congestion control, or authentication. Applications
must add the properties they need. Keep request/response amplification in mind:
an unauthenticated small request that triggers a large response can be abused in
reflection attacks.

QUIC builds authenticated connections, reliable streams, and congestion control
over UDP, normally with TLS 1.3 integrated into the handshake. Independent
streams avoid TCP's cross-stream head-of-line blocking, though packet loss still
affects congestion control. Connection IDs permit path migration. Operators must
allow and observe UDP/443 intentionally; silently blocking it causes HTTP/3 to
fall back rather than proving the application is broken.

## DNS

DNS is a distributed, cached database. A stub resolver asks a recursive
resolver; the recursive resolver follows referrals from root to TLD to
authoritative servers and caches answers for their TTL.

- `A`/`AAAA`: IPv4/IPv6 address.
- `CNAME`: alias to another name; avoid it at a zone apex unless the provider
  offers a standards-aware synthetic record.
- `MX`: mail exchanger; `TXT`: arbitrary text used by SPF and verification;
  `NS`: authoritative servers; `SOA`: zone authority and timers.
- `PTR`: reverse lookup under `in-addr.arpa` or `ip6.arpa`.

NXDOMAIN means the name does not exist; NODATA means the name exists but not for
the requested record type. Negative answers are cached too. Split-horizon DNS
can intentionally return different answers inside and outside a network, so
record the resolver used in every diagnosis.

DNSSEC authenticates DNS data origin and integrity; it does not encrypt queries.
DoT and DoH encrypt the client-to-resolver hop; they do not make a malicious
resolver or authoritative zone honest.

## TLS 1.3 and PKI

TLS gives confidentiality, integrity, and peer authentication according to the
validated certificate and trust store. A client must validate the chain,
hostname, validity interval, signature algorithms, and relevant usage. Disabling
verification to "fix" a certificate error removes the identity guarantee.

In TLS 1.3 the handshake negotiates parameters, authenticates the server
(optionally the client), performs ephemeral key agreement, and derives traffic
keys. Forward secrecy means later compromise of the certificate private key
does not decrypt previously captured sessions. Session resumption reduces round
trips; 0-RTT data is replayable and must be limited to replay-safe operations.

Operational rules: automate certificate rotation, protect private keys, send the
complete intermediate chain, prefer current protocol versions and cipher suites,
and monitor expiry. Certificate pinning raises rotation and recovery risk; use it
only with an explicit backup-pin and rollout plan.

## HTTP and Proxies

HTTP semantics are independent of transport. GET and HEAD are safe; PUT and
DELETE are defined as idempotent; POST is not inherently idempotent. Real retry
safety still depends on application behavior. Use idempotency keys for retried
non-idempotent business operations.

HTTP/1.1 uses a textual message format and persistent connections. HTTP/2
multiplexes binary streams over one TCP connection and compresses headers.
HTTP/3 maps HTTP onto QUIC. Reverse proxies terminate client connections and
forward to upstreams; load balancers distribute work; gateways also apply
cross-cutting policy. Preserve and validate forwarding metadata at a single
trusted boundary—never trust an arbitrary client's `X-Forwarded-For`.

Cache correctness depends on cache keys and directives. Include every
representation-varying input in the key, use `Vary` deliberately, and never
cache user-specific or authorization-sensitive content in a shared cache unless
the policy is proven safe.

## Firewalls, Zero Trust, and Segmentation

A stateful firewall tracks flows and applies rules to new and established
traffic. Keep rules narrow in source, destination, protocol, port, direction,
and identity where possible. Default-deny between trust zones, document each
exception, log decisions at useful boundaries, and remove expired rules.

Zero trust means no implicit trust based solely on network location. Authenticate
workload and user identity, authorize each requested action, encrypt traffic,
minimize standing privilege, and continuously evaluate signals. It does not mean
"put an agent on everything" or eliminate network controls; identity and network
segmentation reinforce each other.

## Authorized Network-Security Assessment

Before testing, record scope (CIDRs, domains, cloud accounts), allowed methods,
time window, rate limits, data-handling rules, emergency contact, and stop
conditions. Discovery should proceed from passive inventory and configuration
review to controlled active verification. Rate-limit scans and explicitly avoid
fragile operational technology unless it is in scope.

A defensible workflow is:

1. Build an asset and trust-boundary inventory from authoritative sources.
2. Verify exposed services and versions without assuming a banner is accurate.
3. Map reachable paths and effective firewall policy from representative zones.
4. Test authentication, authorization, segmentation, TLS, DNS, and management
   planes with the least disruptive technique that proves the claim.
5. Preserve packet captures, timestamps, commands, and configuration evidence.
6. Report the exploit preconditions and business path, then provide a fix and a
   regression test. Do not turn a finding into persistence or unrelated access.

## Packet-Capture and Troubleshooting Method

Start with a concrete five-tuple, timestamp, client, server, and expected
behavior. Capture as close to both endpoints as possible when the middle path is
uncertain. Use display filters after capture rather than an overly narrow capture
filter that discards the evidence.

Diagnostic sequence:

1. Name: what exact DNS answer and TTL did this client receive?
2. Route: what source address and next hop were selected in each direction?
3. Link: did ARP/ND resolve, and is the VLAN/MTU correct?
4. Transport: did the handshake complete; are there resets, loss, or zero
   windows?
5. Security: did TLS identity validation and firewall policy succeed?
6. Application: what request crossed the wire and what response/status/timing
   came back?

Correlate packet evidence with endpoint, proxy, firewall, and application logs.
A timeout is a symptom, not a root cause; identify the last confirmed successful
step and the first missing event.
