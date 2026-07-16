package rag

import (
	"sort"
	"strings"
)

/*
TopicRouter maps a user query to a candidate set of collection names based
on keyword matches. The router is advisory: if no keyword matches, callers
fall back to searching all collections with a stricter threshold.
*/
type TopicRouter struct {
	rules map[string][]string
}

/*
DefaultTopicRouter returns the built-in keyword table for the shipped knowledge
collections. Keywords are matched case-insensitively against the user's message.
*/
func DefaultTopicRouter() *TopicRouter {
	return &TopicRouter{
		rules: map[string][]string{
			"software-engineering": {
				"software engineering", "clean architecture", "clean code",
				"solid", "single responsibility", "open-closed", "open closed",
				"liskov", "interface segregation", "dependency inversion",
				"object-oriented", "object oriented", "oop", "code smell",
				"refactoring", "design pattern", "test-driven", "tdd",
			},
			"computer-networks": {
				"computer network", "networking", "tcp/ip", "tcp", "udp",
				"dns", "tls", "ipv4", "ipv6", "icmp", "arp", "dhcp",
				"bgp", "ospf", "ethernet", "vlan", "subnet", "cidr",
				"routing", "router", "packet", "socket", "quic",
				"network address translation", "firewall", "osi model",
			},
			"cybersecurity": {
				"cybersecurity", "cyber security", "security", "secure coding",
				"vulnerability", "exploit", "exploitation", "cve", "cwe",
				"owasp", "threat model", "attack surface", "penetration test",
				"fuzz", "sanitizer", "memory safety", "buffer overflow",
				"sql injection", "xss", "ssrf", "csrf", "privilege escalation",
			},
			"laravel": {
				"laravel", "eloquent", "artisan", "blade", "tinker",
				"form request", "formrequest", "service container",
			},
			"php": {
				"php", "psr-", "composer", "phpunit", "pest", "enum",
				"readonly", "first-class callable",
			},
			"nestjs": {
				"nestjs", "nest.js", "@nestjs", "@module", "@controller",
				"@injectable", "guard", "interceptor", "pipe", "decorator",
			},
			"tailwind-css": {
				"tailwind", "tailwindcss", "utility class", "@apply",
				"flexbox", "css grid", "@layer", "@theme", "container query",
				"dark mode", "css ", "stylesheet",
			},
			"architectural-patterns": {
				"hexagonal", "ports and adapters", "microservices",
				"event-driven", "event sourcing", "cqrs", "saga",
				"bounded context", "domain-driven", "ddd", "outbox",
				"distributed monolith", "architecture",
			},
			"native-php": {
				"nativephp", "native-php", "native php",
				"native::", "window::open", "window::get",
				"native:install", "native:jump", "native:migrate",
				"native:build", "native:package", "native:serve",
				"native:devices", "native:run",
				"electron desktop", "tauri", "menu::make",
				"childprocess", "child process artisan",
				"globalshortcut", "system tray",
				"nativeappserviceprovider",
			},
			"go-lang": {
				"golang", "go lang", "gofmt", "go mod", "go build", "go run",
				"go test", "go vet", "go install", "go doc", "go.mod",
				"package main", "func main", "func ", "defer ", "go func",
				"fmt.println", "fmt.printf", "fmt.errorf", "fmt.sprintf",
				"net/http", "http.handler", "http.handlerfunc", "http.listenandserve",
				"http.client", "http.request", "http.responsewriter",
				"encoding/json", "json.marshal", "json.unmarshal",
				"json.newdecoder", "json.newencoder",
				"context.context", "context.withcancel", "context.withtimeout",
				"context.withdeadline", "context.withvalue",
				"io.reader", "io.writer", "io.copy", "io.readall",
				"os.open", "os.create", "os.openfile", "os.args", "os.getenv",
				"errors.is", "errors.as", "errors.new", "%w",
				"strings.contains", "strings.split", "strings.builder",
				"strconv.itoa", "strconv.atoi",
				"time.now", "time.duration", "time.parse",
				"log/slog", "slog.info", "slog.error",
				"testing.t", "testing.b", "t.run", "t.helper", "t.errorf", "t.fatalf",
			},
			"cs-fundamentals": {
				"concurrency", "parallelism", "parallel processing",
				"race condition", "data race", "deadlock", "livelock",
				"starvation",
				"mutex", "rwmutex", "semaphore", "atomic operation",
				"sync.mutex", "sync.rwmutex", "sync.waitgroup", "sync.once", "sync.pool",
				"sync/atomic", "atomic.value", "atomic.int",
				"goroutine", "go routine", "channel", "select statement",
				"gomaxprocs", "-race", "go race detector", "go memory model",
				"happens-before",
				"fiber", "fiber::suspend", "fiber::resume",
				"pcntl_fork", "pcntl_wait", "pcntl_waitpid",
				"shmop", "apcu", "opcache",
				"swoole", "reactphp", "amphp", "ext-parallel",
				"stack vs heap", "escape analysis", "garbage collect",
				"thread-safe", "thread safety",
			},
		},
	}
}

/* Add registers an additional collection → keywords binding. */
func (t *TopicRouter) Add(collection string, keywords []string) {
	if t.rules == nil {
		t.rules = map[string][]string{}
	}
	t.rules[collection] = append(t.rules[collection], keywords...)
}

/*
Route returns the collections whose keywords appear in the query. The
returned slice is empty when nothing matches; callers should treat that as
"search all" with a stricter threshold.
*/
func (t *TopicRouter) Route(query string) []string {
	q := strings.ToLower(query)
	var out []string
	for collection, keywords := range t.rules {
		for _, kw := range keywords {
			if strings.Contains(q, kw) {
				out = append(out, collection)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
