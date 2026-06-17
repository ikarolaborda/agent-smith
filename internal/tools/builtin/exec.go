package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

/*
ContainedExec is the OPT-IN, container-contained execution tool from ADR 0003.
It lets the agent drive a fixed set of vulnerability-research apparatus
operations (fuzz / reproduce / triage) so it can reach Tier-1/2 crash evidence,
WITHOUT ever giving the model a host shell.

The security boundary is the container, not trust: every operation runs inside an
ephemeral `docker run --rm` with the rootfs read-only, the network OFF, all Linux
capabilities dropped, no-new-privileges set, resource caps applied, an explicit
container env allowlist (so no host secret is ever forwarded — docker does not
inherit the host environment), and only the workspace + a tmpfs /tmp writable.
The tool is DISABLED by default; enabling it requires an explicit operator opt-in
(`--allow-exec` / Options.AllowExec) and emits a per-command audit line.

Host `sh -c` with cwd+timeout is deliberately NOT used: it is not a sandbox
(absolute paths, symlinks, redirection, inherited credentials, backgrounding,
network exfiltration) — see ADR 0003 threat model.
*/
type ContainedExec struct {
	workspace string
	image     string
	platform  string
	allowExec bool
	maxWall   time.Duration
	maxOutput int
	memoryMB  int
	pidsLimit int
	cpus      string
	user      string
	audit     func(string)
	run       execRunner
	/* mu serializes contained runs: phase 1 allows one at a time (concurrency cap). */
	mu sync.Mutex
}

/* Phase-1 containment defaults. */
const (
	DefaultExecImage          = "php74-asan"
	DefaultExecPlatform       = "linux/amd64"
	DefaultExecMaxWall        = 10 * time.Minute
	DefaultExecMaxOutputBytes = 64 * 1024
	DefaultExecMemoryMB       = 2048
	DefaultExecPidsLimit      = 512
	DefaultExecCPUs           = "2"
	DefaultExecTmpfsMB        = 64
	/* DefaultExecUser is the non-root UID:GID the container runs as (nobody:nogroup). */
	DefaultExecUser = "65534:65534"
)

/* surfaceRe constrains a surface name to a safe token (no path/flag injection). */
var surfaceRe = regexp.MustCompile(`^[a-z0-9_]+$`)

/* execOperations is the closed vocabulary of structured operations. */
var execOperations = map[string]bool{
	"build":     true,
	"fuzz":      true,
	"reproduce": true,
	"triage":    true,
}

/*
execRunner runs a fully-formed, validated container plan and returns the typed
result. It is injected so tests can exercise the tool and its containment policy
without a live docker daemon (mirrors validate.cliRunner).
*/
type execRunner func(ctx context.Context, spec containerSpec) (execResult, error)

/*
containerSpec is the validated, computed plan for one contained run. It is the
single input to buildDockerArgs, so the entire containment policy is expressed
(and unit-tested) as the argv + env derived from this struct.
*/
type containerSpec struct {
	name        string
	image       string
	platform    string
	workspace   string
	memoryMB    int
	pidsLimit   int
	cpus        string
	tmpfsSizeMB int
	user        string
	env         []string
	entrypoint  string
	cmdArgs     []string
	maxWall     time.Duration
	maxOutput   int
}

/*
execResult is the typed, size-bounded outcome of a contained run. All output is
treated as UNTRUSTED and capped; truncation is reported rather than hidden.
*/
type execResult struct {
	ExitStatus   int
	TimedOut     bool
	Signal       string
	Stdout       string
	Stderr       string
	StdoutTrunc  bool
	StderrTrunc  bool
	BytesDropped int
	Wall         time.Duration
}

/*
NewContainedExec builds the tool. allowExec is the master gate; when false the
tool registers but refuses to execute. workspace is the only writable host path
(mounted at /work); it is resolved to an absolute path. The production docker
runner and a stderr audit sink are wired by default and can be overridden.
*/
func NewContainedExec(workspace string, allowExec bool) *ContainedExec {
	abs := workspace
	if abs != "" {
		if resolved, err := filepath.Abs(abs); err == nil {
			abs = resolved
		}
	}
	return &ContainedExec{
		workspace: abs,
		image:     DefaultExecImage,
		platform:  DefaultExecPlatform,
		allowExec: allowExec,
		maxWall:   DefaultExecMaxWall,
		maxOutput: DefaultExecMaxOutputBytes,
		memoryMB:  DefaultExecMemoryMB,
		pidsLimit: DefaultExecPidsLimit,
		cpus:      DefaultExecCPUs,
		user:      DefaultExecUser,
		audit:     stderrAudit,
		run:       dockerRunner(defaultReaper),
	}
}

func (*ContainedExec) Name() string { return "run" }

func (t *ContainedExec) Description() string {
	return "Run a contained vulnerability-research apparatus operation (fuzz|reproduce|triage) " +
		"inside an ephemeral, network-isolated, read-only Docker container mounting " + t.workspace +
		" at /work. Structured only (no arbitrary shell). Disabled unless the operator enabled execution."
}

func (*ContainedExec) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "operation": { "type": "string", "enum": ["build","fuzz","reproduce","triage"], "description": "Apparatus operation. 'build' is operator-only (needs network) and is not executed here." },
    "surface":   { "type": "string", "description": "Target surface (e.g. unserialize, exif). Lowercase token [a-z0-9_]. Required for fuzz/reproduce." },
    "input":     { "type": "string", "description": "For 'reproduce': workspace-relative path to a single crashing input file." },
    "seconds":   { "type": "integer", "description": "Optional wall-clock budget; clamped to the tool maximum." }
  },
  "required": ["operation"]
}`)
}

/* execArgs is the decoded tool call. */
type execArgs struct {
	Operation string `json:"operation"`
	Surface   string `json:"surface"`
	Input     string `json:"input"`
	Seconds   int    `json:"seconds"`
}

/*
Execute validates the request, refuses when disabled or for non-executable
operations, builds the contained plan, runs it under a bounded context, writes a
redacted audit line, and returns the typed result. It never executes when the
master gate is off and never runs an unstructured command.
*/
func (t *ContainedExec) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if !t.allowExec {
		return "run: contained execution is DISABLED. The operator must start agent-smith with --allow-exec " +
			"(CLI) or Options.AllowExec (server) to enable the sandboxed apparatus runner. No command was executed.", nil
	}

	var args execArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("run: invalid args: %w", err)
	}

	op := strings.TrimSpace(args.Operation)
	if !execOperations[op] {
		return "", fmt.Errorf("run: unknown operation %q (allowed: build, fuzz, reproduce, triage)", op)
	}

	if t.workspace == "" {
		return "", errors.New("run: no workspace is configured; a workspace is required to mount /work")
	}

	if err := validateWorkspaceRoot(t.workspace); err != nil {
		return "", err
	}

	if op == "build" {
		return "run: 'build' requires network egress and host image construction; it is NOT exposed through the " +
			"contained runner in phase 1. Build the apparatus image on the host (scripts/build.sh) before fuzzing.", nil
	}

	spec, err := t.buildSpec(op, args)
	if err != nil {
		return "", err
	}

	budget := t.maxWall
	if args.Seconds > 0 {
		if requested := time.Duration(args.Seconds) * time.Second; requested < budget {
			budget = requested
		}
	}

	/* Serialize: phase-1 concurrency cap of one contained run. */
	t.mu.Lock()
	defer t.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	res, err := t.run(runCtx, spec)
	if err != nil {
		t.auditf("op=%s surface=%s error=%v", op, args.Surface, err)
		return "", fmt.Errorf("run: %s failed: %w", op, err)
	}

	t.auditf("op=%s surface=%s exit=%d timed_out=%t bytes_dropped=%d", op, args.Surface, res.ExitStatus, res.TimedOut, res.BytesDropped)
	return formatResult(op, args.Surface, res), nil
}

/*
buildSpec turns a validated operation into the container plan. The in-container
argv per operation is fixed (table-driven), so the model selects an operation and
typed parameters but can never inject a free-form command.
*/
func (t *ContainedExec) buildSpec(op string, args execArgs) (containerSpec, error) {
	name, err := containerName()
	if err != nil {
		return containerSpec{}, err
	}

	spec := containerSpec{
		name:        name,
		image:       t.image,
		platform:    t.platform,
		workspace:   t.workspace,
		memoryMB:    t.memoryMB,
		pidsLimit:   t.pidsLimit,
		cpus:        t.cpus,
		tmpfsSizeMB: DefaultExecTmpfsMB,
		user:        t.user,
		env:         containedEnv(),
		maxWall:     t.maxWall,
		maxOutput:   t.maxOutput,
	}

	switch op {
	case "fuzz":
		if !surfaceRe.MatchString(args.Surface) {
			return containerSpec{}, fmt.Errorf("run: invalid surface %q (expected token [a-z0-9_]+)", args.Surface)
		}
		spec.entrypoint = "/php-src/sapi/cli/php"
		spec.cmdArgs = []string{
			"/work/harnesses/" + args.Surface + "/driver.php",
			"/work/corpus/" + args.Surface,
		}

	case "reproduce":
		if !surfaceRe.MatchString(args.Surface) {
			return containerSpec{}, fmt.Errorf("run: invalid surface %q (expected token [a-z0-9_]+)", args.Surface)
		}
		rel, rerr := t.workspaceRel(args.Input)
		if rerr != nil {
			return containerSpec{}, rerr
		}
		spec.entrypoint = "/php-src/sapi/cli/php"
		spec.cmdArgs = []string{
			"/work/harnesses/" + args.Surface + "/driver.php",
			"/work/" + rel,
		}

	case "triage":
		/* Fixed grep over mounted artifacts; no shell, no user-controlled pattern. */
		spec.entrypoint = "grep"
		spec.cmdArgs = []string{"-rEl", "ERROR: AddressSanitizer|runtime error:", "/work/artifacts"}

	default:
		return containerSpec{}, fmt.Errorf("run: operation %q is not executable", op)
	}

	return spec, nil
}

/*
workspaceRel validates a reproduce input path: it must resolve inside the
workspace and refer to an existing regular file. Returns the cleaned path
relative to the workspace root for use as /work/<rel> in the container.
*/
func (t *ContainedExec) workspaceRel(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("run: reproduce requires an 'input' path (workspace-relative)")
	}

	clean := filepath.Clean(input)
	abs, err := filepath.Abs(filepath.Join(t.workspace, clean))
	if err != nil {
		return "", fmt.Errorf("run: abs path: %w", err)
	}
	if !insideRoot(abs, t.workspace) {
		return "", fmt.Errorf("run: refused: %q is outside the workspace", input)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		rootReal, rerr := filepath.EvalSymlinks(t.workspace)
		if rerr != nil {
			rootReal = t.workspace
		}
		if !insideRoot(real, rootReal) {
			return "", fmt.Errorf("run: refused: %q resolves outside the workspace", input)
		}
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("run: input not found: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("run: refused: %q is a symlink", input)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("run: refused: %q is not a regular file", input)
	}

	rel, err := filepath.Rel(t.workspace, abs)
	if err != nil {
		return "", fmt.Errorf("run: relativize: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

/*
validateWorkspaceRoot is a fail-closed preflight on the host directory about to
be mounted at /work. Even though the mount is read-only, mounting an over-broad
root (/, $HOME) would re-expose host secrets (SSH keys, cloud creds, the docker
socket) to the container, so those roots are refused. The root must be an
existing, canonical directory.
*/
func validateWorkspaceRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("run: refused: workspace %q is not an absolute path", root)
	}

	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("run: refused: workspace %q is unresolvable: %w", root, err)
	}

	info, err := os.Stat(real)
	if err != nil {
		return fmt.Errorf("run: refused: workspace %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("run: refused: workspace %q is not a directory", root)
	}

	clean := filepath.Clean(real)
	if clean == string(filepath.Separator) {
		return errors.New("run: refused: workspace root '/' is too broad to mount")
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if homeClean := filepath.Clean(home); homeClean != "" && clean == homeClean {
			return fmt.Errorf("run: refused: workspace %q is the home directory; choose a project subdirectory", root)
		}
	}

	return nil
}

/* auditf writes one redacted audit line via the configured sink. */
func (t *ContainedExec) auditf(format string, a ...any) {
	if t.audit == nil {
		return
	}
	t.audit(fmt.Sprintf(format, a...))
}

/*
buildDockerArgs derives the full `docker run` argv from a validated spec. This is
the single place the containment policy lives, so the invariants (network off,
read-only rootfs, only workspace + tmpfs writable, dropped capabilities,
no-new-privileges, resource caps, explicit env allowlist, auto-remove) are all
unit-testable here without a docker daemon.
*/
func buildDockerArgs(spec containerSpec) []string {
	args := []string{
		"run",
		"--rm",
		"--name", spec.name,
		/* Network is unconditionally OFF in phase 1 (egress is a phase-4 non-goal). */
		"--network=none",
		/* Immutable base rootfs; only the mounts below are writable. */
		"--read-only",
		/* Drop every Linux capability and forbid privilege escalation. */
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--workdir", "/work",
	}

	/*
		Run as a fixed non-root UID:GID. Combined with the read-only workspace
		mount below the container has no host write surface, so ownership cannot
		affect the host. The default seccomp/AppArmor profile is left in place
		(never set to unconfined).
	*/
	if spec.user != "" {
		args = append(args, "--user="+spec.user)
	}

	if spec.platform != "" {
		args = append(args, "--platform="+spec.platform)
	}

	tmpfsSize := spec.tmpfsSizeMB
	if tmpfsSize <= 0 {
		tmpfsSize = DefaultExecTmpfsMB
	}
	args = append(args, "--tmpfs", fmt.Sprintf("/tmp:rw,size=%dm,mode=1777,nosuid,nodev,noexec", tmpfsSize))

	/*
		The workspace is mounted READ-ONLY: phase-1 operations only read corpus,
		harnesses, and artifacts; their output is captured on the host. A
		read-only bind removes the host-write surface buddy flagged as the main
		residual. /tmp (tmpfs) is the only writable path inside the container.
	*/
	args = append(args, "-v", spec.workspace+":/work:ro")

	if spec.memoryMB > 0 {
		args = append(args,
			fmt.Sprintf("--memory=%dm", spec.memoryMB),
			fmt.Sprintf("--memory-swap=%dm", spec.memoryMB),
		)
	}
	if spec.pidsLimit > 0 {
		args = append(args, fmt.Sprintf("--pids-limit=%d", spec.pidsLimit))
	}
	if spec.cpus != "" {
		args = append(args, "--cpus="+spec.cpus)
	}

	/*
		Explicit container env allowlist only. docker run does NOT inherit the host
		environment unless -e is given, so no SSH/Git/cloud/Docker credential can
		leak; HOME is synthetic.
	*/
	for _, e := range spec.env {
		args = append(args, "-e", e)
	}

	if spec.entrypoint != "" {
		args = append(args, "--entrypoint", spec.entrypoint)
	}
	args = append(args, spec.image)
	args = append(args, spec.cmdArgs...)
	return args
}

/*
containedEnv is the explicit env handed to the CONTAINER (not the host docker
client). Synthetic HOME, fixed minimal PATH, and the AGENT_SMITH_VALIDATION
marker so re-entrant tooling can detect it.

It deliberately does NOT set ASAN_OPTIONS / UBSAN_OPTIONS: the sanitizer policy
belongs to the target apparatus IMAGE, which already chooses the correct posture
(ASan halts on real memory errors; UBSan logs-but-does-not-halt, because PHP 7.4
emits benign UBSan reports at startup — OBS-0001). Forcing UBSan
halt_on_error=1 here previously aborted every run before the harness could fuzz.
*/
func containedEnv() []string {
	return []string{
		"HOME=/tmp",
		"PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"AGENT_SMITH_VALIDATION=1",
	}
}

/*
dockerClientEnv is the minimal env for the HOST docker CLI process. It is distinct
from the container env: the client needs to locate the daemon and its config, but
inherits nothing else from agent-smith's process.
*/
func dockerClientEnv() []string {
	keep := []string{"HOME", "PATH", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"}
	env := make([]string, 0, len(keep)+1)
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "AGENT_SMITH_VALIDATION=1")
	return env
}

/*
dockerRunner is the production execRunner. It locates docker, builds the hardened
argv, runs it with process-group isolation and bounded output, and — because
killing the docker CLI process does NOT kill the container — invokes the reaper
(`docker rm -f`) on timeout to guarantee no orphaned container survives.
*/
func dockerRunner(reaper func(name string)) execRunner {
	return func(ctx context.Context, spec containerSpec) (execResult, error) {
		bin, err := exec.LookPath("docker")
		if err != nil {
			return execResult{}, fmt.Errorf("docker not found on PATH: %w", err)
		}
		return runContainer(ctx, bin, buildDockerArgs(spec), dockerClientEnv(), spec.maxOutput, spec.name, reaper), nil
	}
}

/*
runContainer runs the container process and, because killing the host CLI does
NOT kill the container, invokes the reaper on timeout to force-remove any orphan.
It is the docker-independent orchestration seam exercised by tests.
*/
func runContainer(ctx context.Context, bin string, args, env []string, maxOutput int, name string, reaper func(string)) execResult {
	raw := runProcess(ctx, bin, args, env, maxOutput)
	if raw.timedOut && reaper != nil {
		reaper(name)
	}
	return toExecResult(raw)
}

/* toExecResult maps the OS-level result to the public typed result. */
func toExecResult(raw rawProcResult) execResult {
	return execResult{
		ExitStatus:   raw.exitStatus,
		TimedOut:     raw.timedOut,
		Signal:       raw.signal,
		Stdout:       string(raw.stdout),
		Stderr:       string(raw.stderr),
		StdoutTrunc:  raw.stdoutTrunc,
		StderrTrunc:  raw.stderrTrunc,
		BytesDropped: raw.bytesDropped,
		Wall:         raw.wall,
	}
}

/* defaultReaper force-removes a container by name, bounded so it cannot hang. */
func defaultReaper(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	cmd.Env = dockerClientEnv()
	_ = cmd.Run()
}

/* stderrAudit is the default audit sink: one redacted line to stderr. */
func stderrAudit(line string) {
	fmt.Fprintln(os.Stderr, "[agent-smith exec audit] "+line)
}

/* containerName mints a unique, daemon-safe container name per run. */
func containerName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("run: name entropy: %w", err)
	}
	return "agent-smith-exec-" + hex.EncodeToString(b), nil
}

/*
rawProcResult is the OS-level outcome of runProcess, before it is mapped to the
public execResult.
*/
type rawProcResult struct {
	exitStatus   int
	signal       string
	timedOut     bool
	stdout       []byte
	stderr       []byte
	stdoutTrunc  bool
	stderrTrunc  bool
	bytesDropped int
	wall         time.Duration
}

/*
runProcess executes bin with args in its own process group, captures bounded
stdout/stderr, and on context cancel/timeout kills the WHOLE process group (not
just the leader) so no child is orphaned. It is the OS-level half of containment
and is tested directly with a child-spawning helper, independent of docker.
*/
func runProcess(ctx context.Context, bin string, args []string, env []string, maxOutput int) rawProcResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outW := &capWriter{max: maxOutput}
	errW := &capWriter{max: maxOutput}
	cmd.Stdout = outW
	cmd.Stderr = errW

	/*
		Override the default cancel (which signals only the leader) to SIGKILL the
		whole process GROUP via the negative pid, reaping any children the command
		backgrounded. WaitDelay is a backstop that force-tears-down if a child
		holds the pipes open past the deadline.
	*/
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	res := classifyExit(err)
	if ctx.Err() == context.DeadlineExceeded {
		res.timedOut = true
		if res.signal == "" {
			res.signal = "killed"
		}
	}

	res.stdout = outW.buf
	res.stderr = errW.buf
	res.stdoutTrunc = outW.truncated()
	res.stderrTrunc = errW.truncated()
	res.bytesDropped = outW.dropped + errW.dropped
	res.wall = time.Since(start)
	return res
}

/* classifyExit maps a cmd.Wait() error to exit status and signal. */
func classifyExit(err error) rawProcResult {
	if err == nil {
		return rawProcResult{exitStatus: 0}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res := rawProcResult{exitStatus: ee.ExitCode()}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.signal = ws.Signal().String()
		}
		return res
	}
	return rawProcResult{exitStatus: -1, signal: "wait_error: " + err.Error()}
}

/*
capWriter buffers up to max bytes and counts the rest as dropped, so a runaway
process cannot exhaust memory and truncation is always reported.
*/
type capWriter struct {
	max     int
	buf     []byte
	dropped int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.max <= 0 || len(w.buf) >= w.max {
		w.dropped += len(p)
		return len(p), nil
	}
	room := w.max - len(w.buf)
	if len(p) <= room {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	w.buf = append(w.buf, p[:room]...)
	w.dropped += len(p) - room
	return len(p), nil
}

func (w *capWriter) truncated() bool { return w.dropped > 0 }

/*
formatResult renders the typed result for the model. It surfaces a sanitizer
verdict but never claims a vulnerability: a crash is evidence, not a 0-day.
*/
func formatResult(op, surface string, res execResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "contained run: op=%s", op)
	if surface != "" {
		fmt.Fprintf(&b, " surface=%s", surface)
	}
	fmt.Fprintf(&b, "\nexit_status=%d timed_out=%t", res.ExitStatus, res.TimedOut)
	if res.Signal != "" {
		fmt.Fprintf(&b, " signal=%s", res.Signal)
	}
	fmt.Fprintf(&b, " wall=%s\n", res.Wall.Round(time.Millisecond))
	if res.StdoutTrunc || res.StderrTrunc {
		fmt.Fprintf(&b, "OUTPUT TRUNCATED (bytes_dropped=%d)\n", res.BytesDropped)
	}

	combined := res.Stdout + "\n" + res.Stderr
	if sanitizerHit(combined) {
		b.WriteString("SANITIZER REPORT detected — this is a crash to triage and minimize, NOT a confirmed 0-day until the novelty + supported-branch gates pass.\n")
	} else {
		b.WriteString("no sanitizer report this run (absence of a crash is NOT proof of no bug).\n")
	}

	if s := strings.TrimSpace(res.Stdout); s != "" {
		b.WriteString("\n--- stdout ---\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if s := strings.TrimSpace(res.Stderr); s != "" {
		b.WriteString("\n--- stderr ---\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

/*
sanitizerHit reports whether output contains a MEMORY-SAFETY crash signature.
Only AddressSanitizer signatures count: this apparatus deliberately runs UBSan in
log-but-don't-halt mode because PHP 7.4 emits benign UBSan "runtime error:"
reports during normal operation (OBS-0001/OBS-0002), so a bare UBSan line is NOT
a crash and must not be flagged as one.
*/
func sanitizerHit(s string) bool {
	return strings.Contains(s, "ERROR: AddressSanitizer") ||
		strings.Contains(s, "SUMMARY: AddressSanitizer") ||
		strings.Contains(s, "AddressSanitizer:DEADLYSIGNAL")
}
