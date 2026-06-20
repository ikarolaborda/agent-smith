package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

/*
recordingRunner is an injected execRunner that records whether it ran and with
what spec, so tests can prove the policy without a docker daemon.
*/
type recordingRunner struct {
	calls int
	spec  containerSpec
	ret   execResult
}

func (r *recordingRunner) run(_ context.Context, spec containerSpec) (execResult, error) {
	r.calls++
	r.spec = spec
	return r.ret, nil
}

/* newTestExec builds an enabled tool with a recording runner over a temp workspace. */
func newTestExec(t *testing.T, allowExec bool) (*ContainedExec, *recordingRunner) {
	t.Helper()
	ws := t.TempDir()
	rr := &recordingRunner{}
	tool := NewContainedExec(ws, allowExec)
	tool.run = rr.run
	tool.audit = func(string) {}
	return tool, rr
}

func call(t *testing.T, tool *ContainedExec, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), raw)
}

func TestContainedExec_DisabledByDefault(t *testing.T) {
	tool, rr := newTestExec(t, false)
	out, err := call(t, tool, map[string]any{"operation": "fuzz", "surface": "unserialize"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.calls != 0 {
		t.Fatalf("disabled tool must NOT invoke the runner; got %d calls", rr.calls)
	}
	if !strings.Contains(out, "DISABLED") || !strings.Contains(out, "--allow-exec") {
		t.Fatalf("disabled message should explain how to enable; got: %q", out)
	}
}

func TestContainedExec_BuildIsRefusedNotExecuted(t *testing.T) {
	tool, rr := newTestExec(t, true)
	out, err := call(t, tool, map[string]any{"operation": "build"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.calls != 0 {
		t.Fatalf("build must not be executed by the contained runner; got %d calls", rr.calls)
	}
	if !strings.Contains(out, "build") || !strings.Contains(out, "network") {
		t.Fatalf("build refusal should explain network/host build; got: %q", out)
	}
}

func TestContainedExec_UnknownOperationRejected(t *testing.T) {
	tool, rr := newTestExec(t, true)
	if _, err := call(t, tool, map[string]any{"operation": "rm -rf"}); err == nil {
		t.Fatal("expected unknown operation to error")
	}
	if rr.calls != 0 {
		t.Fatalf("unknown operation must not run; got %d calls", rr.calls)
	}
}

func TestContainedExec_SurfaceValidation(t *testing.T) {
	tool, rr := newTestExec(t, true)
	for _, bad := range []string{"../etc", "a;b", "Foo", "a b", ""} {
		if _, err := call(t, tool, map[string]any{"operation": "fuzz", "surface": bad}); err == nil {
			t.Fatalf("expected surface %q to be rejected", bad)
		}
	}
	if rr.calls != 0 {
		t.Fatalf("invalid surfaces must not run; got %d calls", rr.calls)
	}
}

func TestContainedExec_FuzzBuildsContainedSpec(t *testing.T) {
	tool, rr := newTestExec(t, true)
	if _, err := call(t, tool, map[string]any{"operation": "fuzz", "surface": "unserialize"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("fuzz should invoke the runner once; got %d", rr.calls)
	}
	if rr.spec.entrypoint != "/php-src/sapi/cli/php" {
		t.Fatalf("unexpected entrypoint: %q", rr.spec.entrypoint)
	}
	want := []string{"/work/harnesses/unserialize/driver.php", "/work/corpus/unserialize"}
	if strings.Join(rr.spec.cmdArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("unexpected cmdArgs: %v", rr.spec.cmdArgs)
	}
}

func TestContainedExec_ReproduceRejectsEscape(t *testing.T) {
	tool, rr := newTestExec(t, true)
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "nonexistent.bin"} {
		if _, err := call(t, tool, map[string]any{"operation": "reproduce", "surface": "unserialize", "input": bad}); err == nil {
			t.Fatalf("expected reproduce input %q to be rejected", bad)
		}
	}
	if rr.calls != 0 {
		t.Fatalf("escape inputs must not run; got %d calls", rr.calls)
	}
}

func TestContainedExec_ReproduceAcceptsWorkspaceFile(t *testing.T) {
	tool, rr := newTestExec(t, true)
	crash := filepath.Join(tool.workspace, "crash.bin")
	if err := os.WriteFile(crash, []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	if _, err := call(t, tool, map[string]any{"operation": "reproduce", "surface": "unserialize", "input": "crash.bin"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("reproduce should run once; got %d", rr.calls)
	}
	if got := rr.spec.cmdArgs[len(rr.spec.cmdArgs)-1]; got != "/work/crash.bin" {
		t.Fatalf("reproduce should pass the contained path; got %q", got)
	}
}

/*
TestBuildDockerArgs_ContainmentInvariants asserts every containment flag the
threat model requires is present in the constructed argv, and that no
escape-enabling flag is present.
*/
func TestBuildDockerArgs_ContainmentInvariants(t *testing.T) {
	spec := containerSpec{
		name:        "agent-smith-exec-test",
		image:       "php74-asan",
		platform:    "linux/amd64",
		workspace:   "/home/u/php74-vuln-research",
		memoryMB:    2048,
		pidsLimit:   512,
		cpus:        "2",
		tmpfsSizeMB: 64,
		user:        "65534:65534",
		env:         containedEnv(),
		entrypoint:  "/php-src/sapi/cli/php",
		cmdArgs:     []string{"/work/harnesses/x/driver.php", "/work/corpus/x"},
	}
	args := buildDockerArgs(spec)
	joined := strings.Join(args, " ")

	required := []string{
		"--rm",
		"--network=none",
		"--pull=never",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user=65534:65534",
		"--pids-limit=512",
		"--memory=2048m",
		"--memory-swap=2048m",
		"--cpus=2",
		"--platform=linux/amd64",
		"--workdir /work",
	}
	for _, r := range required {
		if !strings.Contains(joined, r) {
			t.Errorf("missing required containment flag %q in: %s", r, joined)
		}
	}

	forbidden := []string{"--privileged", "--network=host", "--net=host", "--pid=host", "--cap-add", "unconfined", "docker.sock", "-v /:/"}
	for _, f := range forbidden {
		if strings.Contains(joined, f) {
			t.Errorf("forbidden flag %q present in: %s", f, joined)
		}
	}

	/* Exactly one bind mount, read-only, of the workspace at /work. */
	if !strings.Contains(joined, "-v /home/u/php74-vuln-research:/work:ro") {
		t.Errorf("workspace must be the only bind mount and read-only; got: %s", joined)
	}
	if strings.Count(joined, "-v ") != 1 {
		t.Errorf("expected exactly one bind mount; got: %s", joined)
	}
	if !strings.Contains(joined, "--tmpfs /tmp:rw,size=64m,mode=1777,nosuid,nodev,noexec") {
		t.Errorf("tmpfs /tmp must be the only writable path; got: %s", joined)
	}
}

/*
TestBuildDockerArgs_PullNeverFailsClosed proves the run never resolves its image
from a registry: --pull=never must be present, must be the only --pull mode, and
must precede the image positional (a --pull after the image is an argument to the
container, not to docker run, and would silently re-enable the default pull).
*/
func TestBuildDockerArgs_PullNeverFailsClosed(t *testing.T) {
	spec := containerSpec{
		name:      "n",
		image:     "php74-asan",
		workspace: "/ws",
		env:       containedEnv(),
		cmdArgs:   []string{"/work/driver.php"},
	}
	args := buildDockerArgs(spec)

	pullIdx, imageIdx := -1, -1
	for i, a := range args {
		switch {
		case a == "--pull=never":
			pullIdx = i
		case strings.HasPrefix(a, "--pull"):
			t.Errorf("unexpected --pull mode %q; only --pull=never is allowed: %v", a, args)
		case a == "php74-asan":
			imageIdx = i
		}
	}
	if pullIdx == -1 {
		t.Fatalf("--pull=never missing; image could be fetched from a registry: %v", args)
	}
	if imageIdx == -1 {
		t.Fatalf("image positional not found: %v", args)
	}
	if pullIdx >= imageIdx {
		t.Errorf("--pull=never (idx %d) must precede the image positional (idx %d): %v", pullIdx, imageIdx, args)
	}
}

/*
TestBuildDockerArgs_ImageDigestPin proves the optional content pin: when a digest
is set the image positional is the digest (not the mutable tag), it still sits
after --pull=never and before the container args, and an unset digest falls back
to the tag. Together with --pull=never this is what makes a local re-tag of the
apparatus tag fail closed instead of being silently honored.
*/
func TestBuildDockerArgs_ImageDigestPin(t *testing.T) {
	const digest = "sha256:2f081d4445bfc7a11322f38cff8c8939bfd52713e1089efd4a1a7d6262d09219"

	base := containerSpec{
		name:      "n",
		image:     "php74-asan",
		workspace: "/ws",
		env:       containedEnv(),
		cmdArgs:   []string{"/work/driver.php"},
	}

	/* Unpinned: the image positional is the tag. */
	unpinned := buildDockerArgs(base)
	if got := imageReference(base); got != "php74-asan" {
		t.Errorf("unpinned imageReference = %q, want the tag php74-asan", got)
	}
	if !containsArg(unpinned, "php74-asan") {
		t.Errorf("unpinned args must carry the tag: %v", unpinned)
	}

	/* Pinned: the digest replaces the tag as the image positional. */
	pinned := base
	pinned.imageDigest = digest
	args := buildDockerArgs(pinned)

	if got := imageReference(pinned); got != digest {
		t.Errorf("pinned imageReference = %q, want the digest %q", got, digest)
	}
	if containsArg(args, "php74-asan") {
		t.Errorf("pinned args must NOT carry the mutable tag: %v", args)
	}

	pullIdx, digestIdx, cmdIdx := -1, -1, -1
	for i, a := range args {
		switch a {
		case "--pull=never":
			pullIdx = i
		case digest:
			digestIdx = i
		case "/work/driver.php":
			cmdIdx = i
		}
	}
	if pullIdx == -1 || digestIdx == -1 || cmdIdx == -1 {
		t.Fatalf("expected --pull=never, digest, and cmd arg all present: %v", args)
	}
	if !(pullIdx < digestIdx && digestIdx < cmdIdx) {
		t.Errorf("order must be --pull=never(%d) < digest(%d) < cmd(%d): %v", pullIdx, digestIdx, cmdIdx, args)
	}
}

/*
TestWithExpectedImageDigest proves the option trims surrounding whitespace and
that an empty/blank value leaves the runner unpinned (tag resolution preserved).
*/
func TestWithExpectedImageDigest(t *testing.T) {
	t.Setenv("HOME", "/tmp")

	pinned := NewContainedExec("/ws", true, WithExpectedImageDigest("  sha256:abc  "))
	if pinned.imageDigest != "sha256:abc" {
		t.Errorf("imageDigest = %q, want trimmed sha256:abc", pinned.imageDigest)
	}

	blank := NewContainedExec("/ws", true, WithExpectedImageDigest("   "))
	if blank.imageDigest != "" {
		t.Errorf("blank digest must leave the runner unpinned, got %q", blank.imageDigest)
	}

	none := NewContainedExec("/ws", true)
	if none.imageDigest != DefaultExecImageDigest {
		t.Errorf("default imageDigest = %q, want %q", none.imageDigest, DefaultExecImageDigest)
	}
}

/*
TestFormatResult_MissingLocalImageHint proves that a non-zero run whose stderr is
docker's "image absent + --pull=never" failure gets the actionable build-it-first
hint, while a clean run does not, and the raw stderr is still surfaced.
*/
func TestFormatResult_MissingLocalImageHint(t *testing.T) {
	missing := execResult{
		ExitStatus: 125,
		Stderr:     "docker: Error response from daemon: No such image: php74-asan:latest.",
	}
	out := formatResult("fuzz", "scanf", missing)
	if !strings.Contains(out, "LOCAL IMAGE MISSING") {
		t.Errorf("expected missing-image hint for No such image stderr; got: %s", out)
	}
	if !strings.Contains(out, "scripts/build.sh") {
		t.Errorf("hint must tell the operator how to build the image; got: %s", out)
	}
	if !strings.Contains(out, "No such image") {
		t.Errorf("raw stderr must still be surfaced for debugging; got: %s", out)
	}

	clean := execResult{ExitStatus: 0, Stdout: "ok"}
	if strings.Contains(formatResult("fuzz", "scanf", clean), "LOCAL IMAGE MISSING") {
		t.Errorf("must not emit missing-image hint on a successful run")
	}

	/* A non-zero exit for an unrelated reason must NOT be mislabeled as missing-image. */
	other := execResult{ExitStatus: 1, Stderr: "PHP Parse error: syntax error"}
	if strings.Contains(formatResult("fuzz", "scanf", other), "LOCAL IMAGE MISSING") {
		t.Errorf("missing-image hint must not fire on unrelated non-zero exits")
	}
}

/*
TestBuildDockerArgs_EnvAllowlistNoSecrets proves the container env is exactly the
allowlist and that host secrets present in the process environment never leak
into the container args.
*/
func TestBuildDockerArgs_EnvAllowlistNoSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak-me")
	t.Setenv("GITHUB_TOKEN", "ghp_leak")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")

	spec := containerSpec{
		name:      "n",
		image:     "img",
		workspace: "/ws",
		env:       containedEnv(),
	}
	joined := strings.Join(buildDockerArgs(spec), " ")

	for _, secret := range []string{"AWS_SECRET_ACCESS_KEY", "leak-me", "GITHUB_TOKEN", "ghp_leak", "SSH_AUTH_SOCK"} {
		if strings.Contains(joined, secret) {
			t.Errorf("host secret %q leaked into container args: %s", secret, joined)
		}
	}
	for _, want := range []string{"HOME=/tmp", "AGENT_SMITH_VALIDATION=1", "PATH=/usr/local/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected allowlisted env %q in: %s", want, joined)
		}
	}
}

func TestContainedExec_OutputTruncationReported(t *testing.T) {
	tool, rr := newTestExec(t, true)
	rr.ret = execResult{ExitStatus: 0, Stdout: "partial", StdoutTrunc: true, BytesDropped: 4096}
	out, err := call(t, tool, map[string]any{"operation": "fuzz", "surface": "exif"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "TRUNCATED") || !strings.Contains(out, "4096") {
		t.Fatalf("truncation must be reported to the model; got: %q", out)
	}
}

func TestFormatResult_CrashIsNotClaimedAsZeroDay(t *testing.T) {
	out := formatResult("fuzz", "unserialize", execResult{
		ExitStatus: 99,
		Stderr:     "==1==ERROR: AddressSanitizer: heap-buffer-overflow",
	})
	if !strings.Contains(out, "SANITIZER REPORT") {
		t.Fatalf("should surface the sanitizer report; got: %q", out)
	}
	if !strings.Contains(out, "NOT a confirmed 0-day") {
		t.Fatalf("must not let a crash be presented as a 0-day; got: %q", out)
	}
}

/*
TestSanitizerHit_OnlyAddressSanitizer proves bare UBSan output is NOT flagged as a
crash (it is expected benign noise under this apparatus), while real ASan
signatures and a later ASan crash after UBSan noise ARE flagged.
*/
func TestSanitizerHit_OnlyAddressSanitizer(t *testing.T) {
	bareUBSan := "/php-src/ext/mbstring/mbstring.c:784:12: runtime error: applying non-zero offset 1 to null pointer\nSUMMARY: UndefinedBehaviorSanitizer: undefined-behavior"
	if sanitizerHit(bareUBSan) {
		t.Error("bare UBSan output must NOT be flagged as a memory-safety crash")
	}

	for _, asan := range []string{
		"==1==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x...",
		"SUMMARY: AddressSanitizer: heap-use-after-free",
		"AddressSanitizer:DEADLYSIGNAL",
	} {
		if !sanitizerHit(asan) {
			t.Errorf("expected ASan signature to be flagged: %q", asan)
		}
	}

	mixed := bareUBSan + "\n...later...\n==1==ERROR: AddressSanitizer: heap-buffer-overflow"
	if !sanitizerHit(mixed) {
		t.Error("an ASan crash after benign UBSan noise must still be flagged")
	}
}

func TestValidateWorkspaceRoot(t *testing.T) {
	if err := validateWorkspaceRoot("relative/path"); err == nil {
		t.Error("relative workspace must be refused")
	}
	if err := validateWorkspaceRoot(string(filepath.Separator)); err == nil {
		t.Error("root '/' must be refused")
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if err := validateWorkspaceRoot(home); err == nil {
			t.Error("home directory must be refused")
		}
	}
	ok := t.TempDir()
	if err := validateWorkspaceRoot(ok); err != nil {
		t.Errorf("a normal project dir should be accepted; got %v", err)
	}
}

/*
TestRunProcess_ProcessTreeKillOnTimeout proves the OS-level containment half:
when the context times out, the WHOLE process group is killed, so a backgrounded
child does not survive as an orphan. Docker-independent.
*/
func TestRunProcess_ProcessTreeKillOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	/* Background a long-lived grandchild, record its pid, then block. */
	script := "sleep 30 & echo $! > " + pidFile + "; wait"

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := runProcess(ctx, "/bin/sh", []string{"-c", script}, []string{"PATH=/usr/bin:/bin"}, 4096)
	elapsed := time.Since(start)

	if !res.timedOut {
		t.Fatalf("expected timeout; got signal=%q exit=%d", res.signal, res.exitStatus)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("runProcess waited for the child instead of killing the group: %s", elapsed)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Skipf("child pid not recorded (shell timing): %v", err)
	}
	var childPID int
	if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &childPID); err != nil || childPID <= 0 {
		t.Skipf("unparseable child pid %q", string(data))
	}

	/* Poll: the grandchild must be reaped (ESRCH) shortly after the group kill. */
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backgrounded child %d survived the process-group kill (orphan)", childPID)
}

func TestRunProcess_OutputTruncated(t *testing.T) {
	ctx := context.Background()
	res := runProcess(ctx, "/bin/sh", []string{"-c", "printf 'A%.0s' $(seq 1 5000)"}, []string{"PATH=/usr/bin:/bin"}, 100)
	if !res.stdoutTrunc {
		t.Fatal("expected stdout truncation flag")
	}
	if len(res.stdout) > 100 {
		t.Fatalf("captured output exceeded cap: %d", len(res.stdout))
	}
	if res.bytesDropped <= 0 {
		t.Fatal("expected dropped bytes to be counted")
	}
}

func TestRunProcess_ExitStatus(t *testing.T) {
	res := runProcess(context.Background(), "/bin/sh", []string{"-c", "exit 7"}, []string{"PATH=/usr/bin:/bin"}, 1024)
	if res.exitStatus != 7 {
		t.Fatalf("expected exit 7; got %d", res.exitStatus)
	}
	if res.timedOut {
		t.Fatal("clean exit must not be flagged as timeout")
	}
}

/*
TestRunContainer_ReaperFiresOnTimeout proves the orphan-prevention orchestration:
on timeout the reaper (docker rm -f in production) is invoked with the container
name. Uses a sleeping command so no docker daemon is needed.
*/
func TestRunContainer_ReaperFiresOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var reaped string
	reaper := func(name string) { reaped = name }

	res := runContainer(ctx, "/bin/sh", []string{"-c", "sleep 30"}, []string{"PATH=/usr/bin:/bin"}, 1024, "agent-smith-exec-abc", reaper)
	if !res.TimedOut {
		t.Fatalf("expected timeout result")
	}
	if reaped != "agent-smith-exec-abc" {
		t.Fatalf("reaper must force-remove the container on timeout; got %q", reaped)
	}
}

func TestRunContainer_NoReaperOnCleanExit(t *testing.T) {
	var reaped string
	reaper := func(name string) { reaped = name }
	res := runContainer(context.Background(), "/bin/sh", []string{"-c", "exit 0"}, []string{"PATH=/usr/bin:/bin"}, 1024, "n", reaper)
	if res.ExitStatus != 0 {
		t.Fatalf("expected clean exit; got %d", res.ExitStatus)
	}
	if reaped != "" {
		t.Fatalf("reaper must not fire on clean exit; got %q", reaped)
	}
}

/*
TestEveryRunnableOp_CarriesContainmentTrio asserts that fuzz, reproduce, and
triage all produce argv containing the core containment trio — not just the
happy-path fuzz case.
*/
func TestEveryRunnableOp_CarriesContainmentTrio(t *testing.T) {
	tool, rr := newTestExec(t, true)
	crash := filepath.Join(tool.workspace, "c.bin")
	if err := os.WriteFile(crash, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []map[string]any{
		{"operation": "fuzz", "surface": "unserialize"},
		{"operation": "reproduce", "surface": "unserialize", "input": "c.bin"},
		{"operation": "triage"},
	}
	trio := []string{"--user=65534:65534", "--read-only", "--security-opt=no-new-privileges", "--network=none", "--cap-drop=ALL"}
	for _, c := range cases {
		rr.calls = 0
		if _, err := call(t, tool, c); err != nil {
			t.Fatalf("op %v: %v", c["operation"], err)
		}
		joined := strings.Join(buildDockerArgs(rr.spec), " ")
		for _, flag := range trio {
			if !strings.Contains(joined, flag) {
				t.Errorf("op %v missing %q: %s", c["operation"], flag, joined)
			}
		}
	}
}

/*
TestContainerName_InternalAndUnique proves the container name is minted
internally (fixed prefix, never user input) and unique per call, so the reaper's
`docker rm -f <name>` target cannot be influenced by the model.
*/
func TestContainerName_InternalAndUnique(t *testing.T) {
	a, err := containerName()
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	b, err := containerName()
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if !strings.HasPrefix(a, "agent-smith-exec-") {
		t.Fatalf("name must use the fixed internal prefix; got %q", a)
	}
	if a == b {
		t.Fatal("names must be unique per run")
	}
}

func TestNewDefaultRegistry_ExecGatedOff(t *testing.T) {
	if _, err := NewDefaultRegistry(t.TempDir()).Get("run"); err == nil {
		t.Fatal("NewDefaultRegistry must not register the exec tool")
	}
	if _, err := NewDefaultRegistryWithExec("", true).Get("run"); err == nil {
		t.Fatal("exec tool must not register without a workspace")
	}
	if _, err := NewDefaultRegistryWithExec(t.TempDir(), true).Get("run"); err != nil {
		t.Fatal("exec tool should register with allowExec + workspace")
	}
	if _, err := NewDefaultRegistryWithExec(t.TempDir(), false).Get("run"); err == nil {
		t.Fatal("exec tool must stay off when allowExec is false")
	}
}

/*
TestContainedExec_InvalidPinFailsClosed proves a malformed image pin makes the
tool refuse to run (buildSpec errors) rather than handing an ambiguous reference
to docker — the runner must never be invoked.
*/
func TestContainedExec_InvalidPinFailsClosed(t *testing.T) {
	rr := &recordingRunner{}
	tool := NewContainedExec(t.TempDir(), true, WithExpectedImageDigest("php74-asan"))
	tool.run = rr.run
	tool.audit = func(string) {}

	if _, err := call(t, tool, map[string]any{"operation": "fuzz", "surface": "unserialize"}); err == nil {
		t.Fatal("expected a malformed image pin to error before running")
	}
	if rr.calls != 0 {
		t.Fatalf("invalid pin must not invoke the runner; got %d calls", rr.calls)
	}
}

/*
TestValidImagePin proves the pin format guard accepts the content-addressable
forms docker resolves and rejects tags, short IDs, and junk so a malformed pin
fails closed (enforced in buildSpec) instead of reaching docker as an ambiguous arg.
*/
func TestValidImagePin(t *testing.T) {
	id := "2f081d4445bfc7a11322f38cff8c8939bfd52713e1089efd4a1a7d6262d09219"
	good := []string{
		id,
		"sha256:" + id,
		"php74-asan@sha256:" + id,
	}
	for _, s := range good {
		if !validImagePin(s) {
			t.Errorf("validImagePin(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",
		"php74-asan",                    // a tag, not a pin
		"sha256:abc",                    // too short
		"sha256:" + id + "ff",           // too long
		"  sha256:" + id,                // leading space
		"--privileged",                  // flag-shaped
		"sha256:" + strings.ToUpper(id), // uppercase hex
	}
	for _, s := range bad {
		if validImagePin(s) {
			t.Errorf("validImagePin(%q) = true, want false", s)
		}
	}
}

/* containsArg reports whether args contains s as an exact element (not a substring). */
func containsArg(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}
