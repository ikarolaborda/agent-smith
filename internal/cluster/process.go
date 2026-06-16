/*
Supervised external process. The exo, MLX, and llama.cpp backends all need to
launch a long-lived child process and keep it alive subject to a restart
policy. supervisor centralizes that: start, stop, restart-on-failure with a
bounded attempt count, and a readiness probe that waits for the backend's HTTP
endpoint to answer before Start returns.
*/
package cluster

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

/* spawnSpec describes a child process to supervise. */
type spawnSpec struct {
	name    string
	path    string
	args    []string
	env     []string
	workDir string
	/* readyAddr is host:port polled for TCP readiness after launch. */
	readyAddr string
	/* readyTimeout bounds how long Start waits for readiness. */
	readyTimeout time.Duration
}

/*
supervisor owns one child process and restarts it on unexpected exit, up to
maxRestarts, when policy is "on_failure". A clean Stop suppresses restarts.
*/
type supervisor struct {
	spec        spawnSpec
	policy      string
	maxRestarts int
	logger      *slog.Logger
	metrics     *Collector

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	started  bool
	restarts int
	lastErr  error
}

func newSupervisor(spec spawnSpec, policy string, maxRestarts int, logger *slog.Logger, m *Collector) *supervisor {
	return &supervisor{spec: spec, policy: policy, maxRestarts: maxRestarts, logger: logger, metrics: m}
}

/*
Start launches the process and blocks until its endpoint is ready or the
readiness timeout elapses. A background goroutine then watches for exit and
applies the restart policy.
*/
func (s *supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.stopping = false
	s.started = true
	s.mu.Unlock()

	if err := s.launch(); err != nil {
		return err
	}
	go s.watch()

	if s.spec.readyAddr != "" {
		if err := waitForTCP(ctx, s.spec.readyAddr, s.readyTimeout()); err != nil {
			_ = s.Stop(context.Background())
			return fmt.Errorf("%s: endpoint %s not ready: %w", s.spec.name, s.spec.readyAddr, err)
		}
	}
	return nil
}

func (s *supervisor) readyTimeout() time.Duration {
	if s.spec.readyTimeout > 0 {
		return s.spec.readyTimeout
	}
	return 90 * time.Second
}

/* launch starts a fresh exec.Cmd and wires stdout/stderr to the logger. */
func (s *supervisor) launch() error {
	cmd := exec.Command(s.spec.path, s.spec.args...)
	cmd.Dir = s.spec.workDir
	if len(s.spec.env) > 0 {
		cmd.Env = append(os.Environ(), s.spec.env...)
	}
	/*
		Run the child in its own process group so Stop can signal the whole
		tree. This matters for the MLX sidecar, which re-execs mlx.launch and
		spawns further children: killing only the direct child would orphan
		them and leak GPU/unified-memory holders.
	*/
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: stdout pipe: %w", s.spec.name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s: stderr pipe: %w", s.spec.name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: start %s: %w", s.spec.name, s.spec.path, err)
	}
	go pipeLines(s.logger, s.spec.name, stdout)
	go pipeLines(s.logger, s.spec.name, stderr)

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	s.logger.Info("cluster: backend process started", "backend", s.spec.name, "pid", cmd.Process.Pid)
	return nil
}

/* watch blocks on process exit and restarts per policy. */
func (s *supervisor) watch() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	err := cmd.Wait()

	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.lastErr = err
	shouldRestart := s.policy == "on_failure" && s.restarts < s.maxRestarts
	if shouldRestart {
		s.restarts++
	}
	restarts := s.restarts
	s.mu.Unlock()

	s.logger.Warn("cluster: backend process exited", "backend", s.spec.name, "err", err, "restart", shouldRestart, "restarts", restarts)
	if !shouldRestart {
		return
	}
	if s.metrics != nil {
		s.metrics.RecordRestart(s.spec.name)
	}
	/*
		Exponential backoff with jitter between restarts so a runtime that
		crashes immediately on launch (bad model path, OOM) does not spin in a
		tight relaunch loop and amplify the failure. Capped at 30s; the
		max_restart_attempts bound above is the terminal stop.
	*/
	backoff := restartBackoff(restarts)
	s.logger.Info("cluster: backing off before restart", "backend", s.spec.name, "delay", backoff, "attempt", restarts)
	time.Sleep(backoff)
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return
	}
	if err := s.launch(); err != nil {
		s.logger.Error("cluster: backend restart failed", "backend", s.spec.name, "err", err)
		return
	}
	go s.watch()
}

/* Stop terminates the process and suppresses any further restarts. */
func (s *supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	cmd := s.cmd
	s.started = false
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	/*
		Signal the whole process group only when we can confirm the child is its
		own group leader (pgid == pid), which Setpgid guarantees at launch. The
		explicit Getpgid check prevents overreach: if a child reassigned its
		group/session, -pid could target an unrelated group, so we fall back to
		signaling just the child in that case.
	*/
	pid := cmd.Process.Pid
	groupKill := func(sig syscall.Signal) {
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
			_ = syscall.Kill(-pid, sig)
			return
		}
		_ = cmd.Process.Signal(sig)
	}
	groupKill(syscall.SIGINT)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		groupKill(syscall.SIGKILL)
		_ = cmd.Process.Kill()
	case <-ctx.Done():
		groupKill(syscall.SIGKILL)
		_ = cmd.Process.Kill()
	}
	return nil
}

/* RestartCount reports how many times this process has been restarted. */
func (s *supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

/*
restartBackoff returns an exponential delay (1s, 2s, 4s, … capped at 30s) for
the Nth restart attempt, plus up to 25% jitter to avoid synchronized relaunch
storms across backends.
*/
func restartBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 5 {
		shift = 5
	}
	base := time.Second << uint(shift)
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base)/4 + 1))
	return base + jitter
}

/* pipeLines forwards a child's output stream to the structured logger. */
func pipeLines(logger *slog.Logger, name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		logger.Debug("cluster: backend output", "backend", name, "line", sc.Text())
	}
}

/* waitForTCP polls a host:port until it accepts a connection or times out. */
func waitForTCP(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s", timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
