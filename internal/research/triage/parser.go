// Package triage converts bounded hostile worker logs into deterministic crash evidence.
package triage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const (
	MaxLogBytes = 4 << 20
	MaxLogLines = 20000
	MaxFrames   = 100
)

var ErrLogTooLarge = errors.New("research triage: log exceeds parser limit")

var (
	asanError        = regexp.MustCompile(`ERROR: AddressSanitizer: ([A-Za-z0-9_-]+)`)
	asanSummary      = regexp.MustCompile(`SUMMARY: AddressSanitizer: ([A-Za-z0-9_-]+)\s*(.*)`)
	msanWarning      = regexp.MustCompile(`WARNING: MemorySanitizer: ([A-Za-z0-9_-]+)`)
	accessPattern    = regexp.MustCompile(`\b(READ|WRITE) of size ([0-9]+)\b`)
	stackPattern     = regexp.MustCompile(`^\s*#([0-9]+)\s+(0x[0-9a-fA-F]+)\s+in\s+(.+?)(?:\s+([^ ]+):([0-9]+)(?::[0-9]+)?)?\s*$`)
	stackNoIn        = regexp.MustCompile(`^\s*#([0-9]+)\s+(0x[0-9a-fA-F]+)\s+\(([^+()]+)(?:\+[^()]*)?\)\s*$`)
	ubsanPattern     = regexp.MustCompile(`^([^:\n]+):([0-9]+)(?::[0-9]+)?: runtime error: (.+)$`)
	assertionPattern = regexp.MustCompile(`(?i)(assertion .* failed|assert\([^\n]+\) failed)`)
)

// ParseOptions binds parsed evidence to immutable run/artifact identities.
type ParseOptions struct {
	ID              string
	CampaignID      string
	RunID           string
	BuildID         string
	InputArtifactID string
	CreatedAt       time.Time
}

// Parse converts one bounded sanitizer/process log into a normalized observation.
func Parse(log []byte, opts ParseOptions) (domain.CrashObservation, error) {
	if len(log) > MaxLogBytes {
		return domain.CrashObservation{}, ErrLogTooLarge
	}
	if opts.ID == "" || opts.CampaignID == "" || opts.RunID == "" || opts.BuildID == "" {
		return domain.CrashObservation{}, errors.New("research triage: observation, campaign, run, and build ids required")
	}
	if opts.CreatedAt.IsZero() {
		opts.CreatedAt = time.Now().UTC()
	}
	observation := domain.CrashObservation{
		SchemaVersion: 1, ID: opts.ID, CampaignID: opts.CampaignID, RunID: opts.RunID, BuildID: opts.BuildID,
		InputArtifactID: opts.InputArtifactID, Class: domain.ObservationUnclassified, CreatedAt: opts.CreatedAt,
	}
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount > MaxLogLines {
			return domain.CrashObservation{}, ErrLogTooLarge
		}
		line := strings.TrimSpace(scanner.Text())
		classifyLine(&observation, line)
		if len(observation.Frames) < MaxFrames {
			if frame, ok := parseFrame(line); ok {
				observation.Frames = append(observation.Frames, frame)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return domain.CrashObservation{}, fmt.Errorf("research triage: scan log: %w", err)
	}
	if observation.Summary == "" {
		observation.Summary = summaryFor(observation.Class)
	}
	observation.SecurityRelevant = observation.Class == domain.ObservationASanMemory || observation.Class == domain.ObservationMSanMemory
	observation.Signature = Signature(observation)
	return observation, nil
}

func classifyLine(observation *domain.CrashObservation, line string) {
	if match := asanError.FindStringSubmatch(line); len(match) > 0 {
		observation.Class, observation.Sanitizer, observation.BugType = domain.ObservationASanMemory, "address", match[1]
		observation.Summary = "AddressSanitizer: " + match[1]
	}
	if match := asanSummary.FindStringSubmatch(line); len(match) > 0 {
		observation.Class, observation.Sanitizer, observation.BugType = domain.ObservationASanMemory, "address", match[1]
		observation.Summary = strings.TrimSpace("AddressSanitizer: " + match[1] + " " + match[2])
	}
	if match := msanWarning.FindStringSubmatch(line); len(match) > 0 {
		observation.Class, observation.Sanitizer, observation.BugType = domain.ObservationMSanMemory, "memory", match[1]
		observation.Summary = "MemorySanitizer: " + match[1]
	}
	if match := accessPattern.FindStringSubmatch(line); len(match) > 0 {
		observation.Access = strings.ToLower(match[1])
		observation.AccessSize, _ = strconv.ParseInt(match[2], 10, 64)
	}
	if match := ubsanPattern.FindStringSubmatch(line); len(match) > 0 && observation.Class == domain.ObservationUnclassified {
		observation.Class, observation.Sanitizer, observation.BugType = domain.ObservationUBSan, "undefined", normalizeSpace(match[3])
		observation.Summary = "UndefinedBehaviorSanitizer: " + observation.BugType
		lineNumber, _ := strconv.Atoi(match[2])
		observation.Frames = append(observation.Frames, domain.StackFrame{Index: 0, File: match[1], Line: lineNumber})
	}
	lower := strings.ToLower(line)
	if strings.Contains(lower, "libfuzzer: timeout") || strings.Contains(lower, "timeout after") {
		observation.Class, observation.BugType, observation.Summary = domain.ObservationTimeout, "timeout", "worker timeout"
	}
	if strings.Contains(lower, "libfuzzer: out-of-memory") || strings.Contains(lower, "out of memory") || strings.Contains(lower, "out-of-memory") {
		observation.Class, observation.BugType, observation.Summary = domain.ObservationOOM, "out-of-memory", "worker out of memory"
	}
	if strings.Contains(line, "LeakSanitizer: detected memory leaks") {
		observation.Class, observation.Sanitizer, observation.BugType, observation.Summary = domain.ObservationLeak, "leak", "memory-leak", "LeakSanitizer: memory leak"
	}
	if match := assertionPattern.FindString(line); match != "" {
		observation.Class, observation.BugType, observation.Summary = domain.ObservationAssertion, "assertion", normalizeSpace(match)
	}
	if observation.Class == domain.ObservationUnclassified && (strings.Contains(line, "AddressSanitizer:DEADLYSIGNAL") || strings.Contains(lower, "segmentation fault") || strings.Contains(lower, "libfuzzer: deadly signal")) {
		observation.Class, observation.Signal, observation.BugType, observation.Summary = domain.ObservationSignal, "SIGSEGV", "signal", "process terminated by signal"
	}
}

func parseFrame(line string) (domain.StackFrame, bool) {
	if match := stackPattern.FindStringSubmatch(line); len(match) > 0 {
		index, _ := strconv.Atoi(match[1])
		lineNumber, _ := strconv.Atoi(match[5])
		function := normalizeFunction(match[3])
		return domain.StackFrame{Index: index, Address: strings.ToLower(match[2]), Function: function, File: match[4], Line: lineNumber}, true
	}
	if match := stackNoIn.FindStringSubmatch(line); len(match) > 0 {
		index, _ := strconv.Atoi(match[1])
		return domain.StackFrame{Index: index, Address: strings.ToLower(match[2]), Module: filepath.Base(match[3])}, true
	}
	return domain.StackFrame{}, false
}

// Signature creates an address-independent root-cause grouping key.
func Signature(observation domain.CrashObservation) string {
	parts := []string{string(observation.Class), strings.ToLower(observation.BugType), observation.Access, strconv.FormatInt(observation.AccessSize, 10)}
	used := 0
	for _, frame := range observation.Frames {
		if ignoredFrame(frame) {
			continue
		}
		parts = append(parts, normalizeFunction(frame.Function), filepath.Base(frame.File), strconv.Itoa(frame.Line), filepath.Base(frame.Module))
		used++
		if used == 5 {
			break
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func ignoredFrame(frame domain.StackFrame) bool {
	value := strings.ToLower(frame.Function + " " + frame.Module)
	for _, ignored := range []string{"__asan", "__msan", "libfuzzer", "fuzzer::", "start_thread", "libc_start"} {
		if strings.Contains(value, ignored) {
			return true
		}
	}
	return false
}

func normalizeFunction(value string) string {
	value = normalizeSpace(value)
	if index := strings.Index(value, "("); index > 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func normalizeSpace(value string) string { return strings.Join(strings.Fields(value), " ") }

func summaryFor(class domain.ObservationClass) string {
	if class == domain.ObservationUnclassified {
		return "unclassified worker observation"
	}
	return strings.ReplaceAll(string(class), "_", " ")
}
