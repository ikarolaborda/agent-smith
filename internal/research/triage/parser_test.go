package triage

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestParseGoldenObservationClasses(t *testing.T) {
	tests := []struct {
		file       string
		class      domain.ObservationClass
		relevant   bool
		bugType    string
		access     string
		accessSize int64
	}{
		{"asan_heap_oob.log", domain.ObservationASanMemory, true, "heap-buffer-overflow", "write", 1},
		{"ubsan_shift.log", domain.ObservationUBSan, false, "shift exponent 64 is too large for 32-bit type 'unsigned int'", "", 0},
		{"msan_uninitialized.log", domain.ObservationMSanMemory, true, "use-of-uninitialized-value", "", 0},
		{"timeout.log", domain.ObservationTimeout, false, "timeout", "", 0},
		{"oom.log", domain.ObservationOOM, false, "out-of-memory", "", 0},
		{"assertion.log", domain.ObservationAssertion, false, "assertion", "", 0},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			log, err := os.ReadFile("testdata/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := Parse(log, ParseOptions{ID: "observation", CampaignID: "campaign", RunID: "run", BuildID: "build", InputArtifactID: "input"})
			if err != nil {
				t.Fatal(err)
			}
			if observation.Class != test.class || observation.SecurityRelevant != test.relevant || observation.BugType != test.bugType || observation.Access != test.access || observation.AccessSize != test.accessSize {
				t.Fatalf("observation=%#v", observation)
			}
			if !strings.HasPrefix(observation.Signature, "sha256:") {
				t.Fatalf("signature=%q", observation.Signature)
			}
		})
	}
}

func TestSignatureIgnoresAddressesAndSanitizerFrames(t *testing.T) {
	base := domain.CrashObservation{Class: domain.ObservationASanMemory, BugType: "heap-buffer-overflow", Access: "write", AccessSize: 1, Frames: []domain.StackFrame{
		{Index: 0, Address: "0x111", Function: "__asan_report_store1"},
		{Index: 1, Address: "0x222", Function: "parse(unsigned char*)", File: "/one/parser.cc", Line: 12},
	}}
	other := base
	other.Frames = []domain.StackFrame{
		{Index: 0, Address: "0xaaa", Function: "__asan_report_store1"},
		{Index: 1, Address: "0xbbb", Function: "parse(unsigned char*)", File: "/two/parser.cc", Line: 12},
	}
	if Signature(base) != Signature(other) {
		t.Fatalf("address/path roots changed signature: %s != %s", Signature(base), Signature(other))
	}
}

func TestParserAndEvidenceLimits(t *testing.T) {
	_, err := Parse(make([]byte, MaxLogBytes+1), ParseOptions{ID: "o", CampaignID: "c", RunID: "r", BuildID: "b"})
	if !errors.Is(err, ErrLogTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
	attempt := domain.CrashObservation{ID: "o", Signature: "sig", InputArtifactID: "input"}
	if facts := ReproductionFacts([]domain.CrashObservation{attempt, attempt}, 3); facts.ReproductionCount != 2 || facts.CrashMachineParsed {
		t.Fatalf("premature reproduction facts=%#v", facts)
	}
	if facts := ReproductionFacts([]domain.CrashObservation{attempt, attempt, attempt}, 3); facts.ReproductionCount != 3 || !facts.CrashMachineParsed {
		t.Fatalf("reproduction facts=%#v", facts)
	}
	mismatch := attempt
	mismatch.Signature = "other"
	if facts := ReproductionFacts([]domain.CrashObservation{attempt, mismatch, attempt}, 3); facts.ReproductionCount != 0 {
		t.Fatalf("mismatch facts=%#v", facts)
	}
}

func TestCrashGroupingDeduplicatesObservationID(t *testing.T) {
	observation := domain.CrashObservation{ID: "observation", CampaignID: "campaign", Signature: "signature", InputArtifactID: "input"}
	group, err := AddToGroup(domain.CrashGroup{}, observation, "group", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	group, err = AddToGroup(group, observation, "", time.Time{})
	if err != nil || len(group.ObservationIDs) != 1 {
		t.Fatalf("group=%#v err=%v", group, err)
	}
	other := observation
	other.ID, other.Signature = "other", "different"
	if _, err := AddToGroup(group, other, "", time.Time{}); err == nil {
		t.Fatal("accepted mismatched signature")
	}
}

func FuzzParseNeverPanics(f *testing.F) {
	f.Add([]byte("==1==ERROR: AddressSanitizer: heap-use-after-free\n#0 0x1 in parse /tmp/a.cc:1"))
	f.Add([]byte("random\x00worker output"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > MaxLogBytes+1 {
			input = input[:MaxLogBytes+1]
		}
		_, _ = Parse(input, ParseOptions{ID: "o", CampaignID: "c", RunID: "r", BuildID: "b"})
	})
}
