package lighthouse

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
)

// ErrMemoryShed is returned by LocalRunner.Run when free memory is
// below LIGHTHOUSE_MEMORY_SHED_THRESHOLD_MB. The consumer treats this
// like a shutdown cancellation: leave the lighthouse_runs row in
// 'running' so XAUTOCLAIM redelivers it once memory recovers, and skip
// the XAck so the Redis stream entry survives.
var ErrMemoryShed = errors.New("lighthouse runner shed audit due to low memory")

// stderrTailBytes caps the bytes we keep from a failing Chromium run.
// 16 KiB is enough to capture the trailing stack/protocol-error context
// without bloating lighthouse_runs.error_message; the column is plain
// TEXT and would otherwise grow unbounded.
const stderrTailBytes = 16 * 1024

// transientStderrSubstrings flag a Chromium failure as worth one retry
// rather than a permanent fail. These match the patterns Lighthouse
// emits when its CDP attach to Chromium drops or the renderer crashes
// — both well-known transient failure modes that recover on a fresh
// browser. Anything outside this list is a hard failure.
var transientStderrSubstrings = []string{
	"Inspector.targetCrashed",
	"Protocol error",
	"Page crashed",
	"WebSocket is not open",
	"Target closed",
}

// LocalRunnerConfig captures the bits the local runner needs from
// the analysis service config. Kept separate from cmd/analysis so the
// runner is testable without the whole consumer scaffolding.
type LocalRunnerConfig struct {
	LighthouseBin string
	ChromiumBin   string
	Provider      archive.ColdStorageProvider
	Bucket        string
	MemoryShedMB  int
	ProfilePreset Profile // defaults to ProfileMobile if empty
}

// LocalRunner shells out to the bundled lighthouse CLI to perform real
// audits. Implements the Runner interface. Phase 3's only Runner
// alternative to StubRunner.
type LocalRunner struct {
	cfg LocalRunnerConfig
	// readMemAvailableMB and now are pluggable for tests so we can
	// exercise the memory-shed and timeout paths without wrangling
	// /proc/meminfo or wall-clock time.
	readMemAvailableMB func() (int, error)
	now                func() time.Time
}

// NewLocalRunner constructs a LocalRunner with the supplied config.
// Returns an error if the config is missing pieces the runner needs to
// operate (binary paths, bucket, archive provider) — better to fail at
// boot than to discover the missing config on the first audit.
func NewLocalRunner(cfg LocalRunnerConfig) (*LocalRunner, error) {
	if strings.TrimSpace(cfg.LighthouseBin) == "" {
		return nil, errors.New("local runner: LighthouseBin is required")
	}
	if strings.TrimSpace(cfg.ChromiumBin) == "" {
		return nil, errors.New("local runner: ChromiumBin is required")
	}
	if cfg.Provider == nil {
		return nil, errors.New("local runner: archive provider is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("local runner: bucket is required")
	}
	if cfg.ProfilePreset == "" {
		cfg.ProfilePreset = ProfileMobile
	}
	return &LocalRunner{
		cfg:                cfg,
		readMemAvailableMB: defaultReadMemAvailableMB,
		now:                time.Now,
	}, nil
}

// Run executes a single Lighthouse audit, parses the JSON result,
// uploads the gzipped raw report to R2, and returns the populated
// AuditResult. Honours req.Timeout via context wrapping; the caller's
// XAck/Fail flow on the consumer side decides what to do with errors.
//
// Retry policy: at most one retry on a transient Chromium failure,
// recognised via stderr substring match. After that, the most recent
// error (with stderr tail) is propagated up.
func (l *LocalRunner) Run(ctx context.Context, req AuditRequest) (AuditResult, error) {
	if l.cfg.MemoryShedMB > 0 {
		avail, err := l.readMemAvailableMB()
		if err != nil {
			// /proc/meminfo missing is a packaging bug; log via the
			// shared lighthouse logger and continue rather than
			// blocking every audit on a transient read.
			lighthouseLog.Warn("memory shed check failed; proceeding",
				"error", err, "run_id", req.RunID, "job_id", req.JobID,
			)
		} else if avail < l.cfg.MemoryShedMB {
			lighthouseLog.Info("memory shed: deferring audit",
				"run_id", req.RunID, "job_id", req.JobID,
				"available_mb", avail, "threshold_mb", l.cfg.MemoryShedMB,
			)
			return AuditResult{}, ErrMemoryShed
		}
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	start := l.now()

	stdout, runErr := l.runOnce(ctx, req)
	if runErr != nil && isTransientErr(runErr) && ctx.Err() == nil {
		lighthouseLog.Warn("lighthouse transient failure; retrying once",
			"run_id", req.RunID, "job_id", req.JobID, "error", runErr,
		)
		stdout, runErr = l.runOnce(ctx, req)
	}
	if runErr != nil {
		return AuditResult{}, runErr
	}

	result, parseErr := ParseReport(stdout)
	if parseErr != nil {
		return AuditResult{}, parseErr
	}

	key, uploadErr := l.uploadReport(ctx, req, stdout)
	if uploadErr != nil {
		return AuditResult{}, uploadErr
	}
	result.ReportKey = key
	result.Duration = l.now().Sub(start)

	return result, nil
}

// runOnce performs a single shellout to the lighthouse CLI, capturing
// stdout (the JSON report) and a capped tail of stderr. On non-zero
// exit it returns an error wrapping the stderr tail so callers can
// surface it into lighthouse_runs.error_message.
func (l *LocalRunner) runOnce(ctx context.Context, req AuditRequest) ([]byte, error) {
	profile := req.Profile
	if profile == "" {
		profile = l.cfg.ProfilePreset
	}

	// Lighthouse 12.x's --preset flag only accepts 'desktop',
	// 'experimental', or 'perf'. Mobile is the implicit default and
	// passing --preset=mobile fails the CLI's argument validation
	// before Chromium even launches. So: pass --preset=desktop only
	// for the desktop profile; otherwise omit the flag entirely.
	args := []string{
		"--output=json",
		"--quiet",
		"--max-wait-for-load=45000",
		"--chrome-flags=--headless=new --no-sandbox --disable-gpu",
	}
	if profile == ProfileDesktop {
		args = append(args, "--preset=desktop")
	}
	args = append(args, req.URL)

	// #nosec G204 -- LighthouseBin is sourced from trusted env config
	// baked into the analysis image at build time, not user input. The
	// only positional arg is req.URL which Lighthouse itself validates;
	// every flag is hard-coded above.
	cmd := exec.CommandContext(ctx, l.cfg.LighthouseBin, args...)
	// Setpgid puts Chromium and any helper renderers into their own
	// process group so a context-cancelled Run can kill the whole tree
	// with a single negative-pid signal. Without this a dying parent
	// orphans renderers that keep eating memory.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CHROMIUM_BIN tells lighthouse which binary to launch. Lighthouse
	// otherwise tries to discover Chrome via $PATH and platform-specific
	// fallbacks, which inside a slim container often means failure.
	cmd.Env = append(os.Environ(),
		"CHROME_PATH="+l.cfg.ChromiumBin,
	)
	var stdout bytes.Buffer
	stderr := newTailBuffer(stderrTailBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	startErr := cmd.Start()
	if startErr != nil {
		return nil, fmt.Errorf("start lighthouse: %w", startErr)
	}

	pgid := cmd.Process.Pid
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		// Kill the whole process group; ignore errors because the
		// process may already have exited between the select fire and
		// the syscall.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitDone
		return nil, ctx.Err()
	case waitErr := <-waitDone:
		if waitErr != nil {
			tail := stderr.tail()
			return nil, fmt.Errorf("lighthouse exit: %w (stderr: %s)",
				waitErr, truncateForLog(tail))
		}
	}

	return stdout.Bytes(), nil
}

// uploadReport gzips the raw lighthouse JSON and uploads it to the
// configured cold store, returning the object key written to
// lighthouse_runs.report_key.
func (l *LocalRunner) uploadReport(ctx context.Context, req AuditRequest, raw []byte) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return "", fmt.Errorf("gzip lighthouse report: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("gzip close: %w", err)
	}

	profile := string(req.Profile)
	if profile == "" {
		profile = string(l.cfg.ProfilePreset)
	}

	key := archive.LighthouseObjectPath(req.JobID, req.SourceTaskID, profile, req.RunID)

	uploadErr := l.cfg.Provider.Upload(ctx, l.cfg.Bucket, key, &buf, archive.UploadOptions{
		ContentType:     "application/json",
		ContentEncoding: "gzip",
		Metadata: map[string]string{
			"run_id":  strconv.FormatInt(req.RunID, 10),
			"job_id":  req.JobID,
			"profile": profile,
		},
	})
	if uploadErr != nil {
		return "", fmt.Errorf("upload lighthouse report: %w", uploadErr)
	}
	return key, nil
}

// isTransientErr decides whether a runOnce failure is worth one retry.
// Context cancellation/deadline are never retried — those are caller
// signals. Everything else is checked against the stderr substring
// allowlist so Chromium's well-known transient crashes get a second
// shot but a missing-binary or argument error fails fast.
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	for _, s := range transientStderrSubstrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// tailBuffer keeps only the last `cap` bytes written to it. Streaming
// long Chromium stderr through a regular bytes.Buffer would blow up
// memory on a wedged audit (Lighthouse's debug output runs to
// megabytes); a ring buffer caps the cost while preserving the most
// recent context, which is what diagnostics need.
type tailBuffer struct {
	cap  int
	buf  []byte
	full bool
	pos  int
}

func newTailBuffer(cap int) *tailBuffer {
	return &tailBuffer{cap: cap, buf: make([]byte, 0, cap)}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= t.cap {
		// Single write larger than cap — keep only the last cap bytes.
		t.buf = append(t.buf[:0], p[n-t.cap:]...)
		t.full = true
		t.pos = 0
		return n, nil
	}
	if !t.full && len(t.buf)+n <= t.cap {
		t.buf = append(t.buf, p...)
		if len(t.buf) == t.cap {
			t.full = true
			t.pos = 0
		}
		return n, nil
	}
	// At cap — wrap.
	if !t.full {
		t.buf = append(t.buf, make([]byte, t.cap-len(t.buf))...)
		t.full = true
		t.pos = 0
	}
	for i := 0; i < n; i++ {
		t.buf[t.pos] = p[i]
		t.pos = (t.pos + 1) % t.cap
	}
	return n, nil
}

// tail returns the buffer's contents in chronological order.
func (t *tailBuffer) tail() []byte {
	if !t.full {
		out := make([]byte, len(t.buf))
		copy(out, t.buf)
		return out
	}
	out := make([]byte, t.cap)
	copy(out, t.buf[t.pos:])
	copy(out[t.cap-t.pos:], t.buf[:t.pos])
	return out
}

// truncateForLog keeps stderr error text small enough to log without
// blowing the structured-log line size limit on Grafana.
func truncateForLog(b []byte) string {
	const max = 2048
	if len(b) <= max {
		return string(b)
	}
	return "..." + string(b[len(b)-max:])
}

// defaultReadMemAvailableMB reads /proc/meminfo's MemAvailable line and
// returns the available memory in megabytes. On non-Linux platforms or
// when the file is unreadable it returns an error so the caller can
// log and continue rather than treating that as zero free memory.
func defaultReadMemAvailableMB() (int, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		// Format: "MemAvailable:    1234567 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemAvailable line: %q", line)
		}
		kb, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", parseErr)
		}
		return kb / 1024, nil
	}
	return 0, errors.New("MemAvailable not found in /proc/meminfo")
}

// Compile-time interface check: LocalRunner satisfies Runner. Without
// this an accidental signature drift on the interface side would only
// surface at the consumer call site.
var _ Runner = (*LocalRunner)(nil)
