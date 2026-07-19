package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

// CommandExecutor makes Docker preflight and argv construction testable.
type CommandExecutor interface {
	Run(context.Context, string, []string, io.Writer, io.Writer) error
}

type osCommandExecutor struct{}

func (osCommandExecutor) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = stdout, stderr
	return command.Run()
}

// DockerOptions configure the rootless Docker/gVisor backend.
type DockerOptions struct {
	Binary          string
	Runtime         string
	RequireRootless bool
	OutputLimit     int64
	Executor        CommandExecutor
}

// DockerBackend executes the fixed apparatus dispatcher without a shell.
type DockerBackend struct {
	binary          string
	runtime         string
	requireRootless bool
	outputLimit     int64
	executor        CommandExecutor
	assurance       Assurance
}

func NewDockerBackend(opts DockerOptions) *DockerBackend {
	if opts.Binary == "" {
		opts.Binary = "docker"
	}
	if opts.OutputLimit <= 0 {
		opts.OutputLimit = defaultCapturedOutputLimit
	}
	if opts.Executor == nil {
		opts.Executor = osCommandExecutor{}
	}
	return &DockerBackend{binary: opts.Binary, runtime: opts.Runtime, requireRootless: opts.RequireRootless, outputLimit: opts.OutputLimit, executor: opts.Executor}
}

// Preflight refuses an unverifiable daemon/runtime posture.
func (d *DockerBackend) Preflight(ctx context.Context) (Assurance, error) {
	var stdout, stderr bytes.Buffer
	if err := d.executor.Run(ctx, d.binary, []string{"info", "--format", "{{json .SecurityOptions}}"}, &stdout, &stderr); err != nil {
		return Assurance{}, fmt.Errorf("docker info: %w: %s", err, boundedMessage(stderr.String()))
	}
	var securityOptions []string
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &securityOptions); err != nil {
		return Assurance{}, fmt.Errorf("decode Docker security options: %w", err)
	}
	rootless, seccomp := false, false
	for _, option := range securityOptions {
		lower := strings.ToLower(option)
		rootless = rootless || strings.Contains(lower, "rootless")
		seccomp = seccomp || strings.Contains(lower, "seccomp")
	}
	if d.requireRootless && !rootless {
		return Assurance{}, errors.New("rootless Docker is required but not reported by the daemon")
	}
	if !seccomp {
		return Assurance{}, errors.New("Docker built-in seccomp is required but not reported by the daemon")
	}
	stdout.Reset()
	stderr.Reset()
	if err := d.executor.Run(ctx, d.binary, []string{"info", "--format", "{{json .CgroupDriver}} {{json .CgroupVersion}}"}, &stdout, &stderr); err != nil {
		return Assurance{}, fmt.Errorf("docker cgroup preflight: %w: %s", err, boundedMessage(stderr.String()))
	}
	var cgroupDriver, cgroupVersionValue string
	if _, err := fmt.Fscan(strings.NewReader(stdout.String()), &cgroupDriver, &cgroupVersionValue); err != nil {
		return Assurance{}, errors.New("Docker cgroup driver/version could not be verified")
	}
	cgroupDriver = strings.Trim(cgroupDriver, `"`)
	cgroupVersion, versionErr := strconv.Atoi(strings.Trim(cgroupVersionValue, `"`))
	if cgroupDriver == "" || versionErr != nil || cgroupVersion <= 0 {
		return Assurance{}, errors.New("Docker cgroup driver/version could not be verified")
	}
	if rootless && cgroupVersion != 2 {
		return Assurance{}, errors.New("rootless Docker requires cgroup v2 for enforceable worker limits")
	}
	isolation := "docker"
	if rootless {
		isolation = "rootless_docker"
	}
	if d.runtime != "" {
		stdout.Reset()
		stderr.Reset()
		if err := d.executor.Run(ctx, d.binary, []string{"info", "--format", "{{json .Runtimes}}"}, &stdout, &stderr); err != nil {
			return Assurance{}, fmt.Errorf("docker runtime preflight: %w", err)
		}
		var runtimes map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &runtimes); err != nil {
			return Assurance{}, fmt.Errorf("decode Docker runtimes: %w", err)
		}
		if _, ok := runtimes[d.runtime]; !ok {
			return Assurance{}, fmt.Errorf("configured Docker runtime %q is unavailable", d.runtime)
		}
		if d.runtime == "runsc" {
			isolation = "gvisor"
		} else {
			isolation += "+" + d.runtime
		}
	}
	d.assurance = Assurance{Backend: "docker", Isolation: isolation, Runtime: d.runtime, Rootless: rootless, Seccomp: "builtin", CgroupVersion: cgroupVersion}
	if d.assurance.Runtime == "" {
		d.assurance.Runtime = "runc"
	}
	return d.assurance, nil
}

func (d *DockerBackend) Execute(ctx context.Context, job domain.WorkerJob, staging string) (Execution, error) {
	started := time.Now()
	if d.assurance.Isolation == "" {
		return Execution{}, errors.New("docker backend used before successful preflight")
	}
	if err := job.Budget.Validate(); err != nil {
		return Execution{}, fmt.Errorf("docker backend: %w", err)
	}
	if !digestPattern.MatchString(job.ImageDigest) {
		return Execution{}, errors.New("docker backend requires exact image digest")
	}
	var inspectOut, inspectErr bytes.Buffer
	if err := d.executor.Run(ctx, d.binary, []string{"image", "inspect", "--format", "{{.Id}}", job.ImageDigest}, &inspectOut, &inspectErr); err != nil {
		return Execution{}, fmt.Errorf("inspect apparatus image: %w: %s", err, boundedMessage(inspectErr.String()))
	}
	if strings.TrimSpace(inspectOut.String()) != job.ImageDigest {
		return Execution{}, errors.New("resolved apparatus image does not match required digest")
	}

	containerName := "agent-smith-" + safeID(job.RunID)
	cpuRate := float64(job.Budget.MaxCPUSeconds) / float64(job.Budget.MaxWallSeconds)
	if cpuRate < 0.001 {
		return Execution{}, errors.New("docker backend: CPU budget is too small for the wall-clock envelope")
	}
	args := []string{
		"run", "--rm", "--name", containerName,
		"--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--pids-limit", strconv.FormatInt(job.Budget.MaxPIDs, 10),
		"--memory", strconv.FormatInt(job.Budget.MaxMemoryBytes, 10), "--memory-swap", strconv.FormatInt(job.Budget.MaxMemoryBytes, 10),
		"--cpus", strconv.FormatFloat(cpuRate, 'f', 6, 64),
		"--ulimit", "core=0:0", "--ulimit", "nofile=1024:1024", "--ulimit", "fsize=" + strconv.FormatInt(job.Budget.MaxDiskBytes, 10) + ":" + strconv.FormatInt(job.Budget.MaxDiskBytes, 10),
		// In rootless mode container uid 0 maps to the unprivileged daemon user,
		// allowing writes to the private host staging directory without granting
		// host root. Capabilities and privilege escalation remain disabled.
		"--user", "0:0", "--workdir", "/work",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=67108864,mode=0700",
		"--mount", dockerMount(staging, "/out", false),
		"--env", "HOME=/tmp", "--env", "PATH=/usr/local/bin:/usr/bin:/bin", "--env", "AGENT_SMITH_RESEARCH=1",
		"--label", "agent-smith.research.run=" + job.RunID,
	}
	if d.runtime != "" {
		args = append(args, "--runtime", d.runtime)
	}
	for _, mount := range job.Mounts {
		args = append(args, "--mount", dockerMount(mount.HostPath, mount.ContainerPath, mount.ReadOnly))
	}
	environmentKeys := make([]string, 0, len(job.Environment))
	for key := range job.Environment {
		environmentKeys = append(environmentKeys, key)
	}
	sort.Strings(environmentKeys)
	for _, key := range environmentKeys {
		if !safeEnvironmentKey(key) || strings.ContainsRune(job.Environment[key], 0) {
			return Execution{}, fmt.Errorf("invalid apparatus environment %q", key)
		}
		args = append(args, "--env", key+"="+job.Environment[key])
	}
	args = append(args, job.ImageDigest, "/apparatus/dispatch", string(job.Operation))
	keys := make([]string, 0, len(job.Arguments))
	for key := range job.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := job.Arguments[key]
		if !safeArgument(key) || strings.ContainsRune(value, 0) || len(value) > 4096 {
			return Execution{}, fmt.Errorf("invalid apparatus argument %q", key)
		}
		args = append(args, "--"+key, value)
	}
	stdout := &limitWriter{limit: d.outputLimit}
	stderr := &limitWriter{limit: d.outputLimit}
	err := d.executor.Run(ctx, d.binary, args, stdout, stderr)
	if ctx.Err() != nil {
		var discard bytes.Buffer
		_ = d.executor.Run(context.Background(), d.binary, []string{"rm", "-f", containerName}, &discard, &discard)
	}
	exit := domain.RunExit{}
	status := domain.RunCompleted
	if err != nil {
		status = domain.RunFailed
		exit.Reason = err.Error()
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exit.Code = exitError.ExitCode()
		} else {
			exit.Code = -1
		}
	}
	return Execution{
		Status: status, Exit: exit, Usage: domain.ResourceUsage{WallMillis: time.Since(started).Milliseconds()},
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
		BytesDropped: stdout.dropped + stderr.dropped,
		Apparatus: domain.ApparatusIdentity{ImageDigest: job.ImageDigest, Runtime: d.assurance.Runtime,
			ManifestID: job.Arguments["manifest"], TargetRevision: job.Arguments["revision"], Harness: job.Arguments["harness"], Sanitizer: job.Arguments["sanitizer"]},
	}, err
}

func dockerMount(source, destination string, readOnly bool) string {
	value := "type=bind,src=" + source + ",dst=" + destination
	if readOnly {
		value += ",readonly"
	}
	return value
}

func safeArgument(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '-' && character != '_' {
					return false
				}
			}
		}
	}
	return true
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

type limitWriter struct {
	data      bytes.Buffer
	limit     int64
	dropped   int64
	truncated bool
}

func (w *limitWriter) Write(input []byte) (int, error) {
	original := len(input)
	remaining := w.limit - int64(w.data.Len())
	if remaining > 0 {
		keep := int64(len(input))
		if keep > remaining {
			keep = remaining
		}
		_, _ = w.data.Write(input[:keep])
	}
	if int64(original) > remaining {
		dropped := int64(original) - max(remaining, 0)
		w.dropped += dropped
		w.truncated = true
	}
	return original, nil
}

func (w *limitWriter) Bytes() []byte { return w.data.Bytes() }
