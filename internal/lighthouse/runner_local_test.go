package lighthouse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Harvey-AU/hover/internal/archive"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider mirrors the archive.ColdStorageProvider used elsewhere in
// the repo (internal/jobs/html_persister_test.go). Kept simple — we
// only need to capture the upload key and payload.
type fakeProvider struct {
	mu        sync.Mutex
	uploads   []fakeUpload
	uploadErr error
}

type fakeUpload struct {
	bucket  string
	key     string
	payload []byte
	opts    archive.UploadOptions
}

func (f *fakeProvider) Upload(_ context.Context, bucket, key string, data io.Reader, opts archive.UploadOptions) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	body, _ := io.ReadAll(data)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, fakeUpload{bucket: bucket, key: key, payload: body, opts: opts})
	return nil
}

func (f *fakeProvider) Ping(_ context.Context, _ string) error { return nil }
func (f *fakeProvider) Download(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeProvider) Exists(_ context.Context, _, _ string) (bool, error) { return true, nil }
func (f *fakeProvider) Provider() string                                    { return "fake" }

const sampleLighthouseJSON = `{
  "categories": { "performance": { "score": 0.91 } },
  "audits": {
    "largest-contentful-paint":  { "numericValue": 2100 },
    "cumulative-layout-shift":   { "numericValue": 0.05 },
    "interaction-to-next-paint": { "numericValue": 150 },
    "total-blocking-time":       { "numericValue": 200 },
    "first-contentful-paint":    { "numericValue": 1100 },
    "speed-index":               { "numericValue": 2700 },
    "server-response-time":      { "numericValue": 280 },
    "total-byte-weight":         { "numericValue": 1200000 }
  }
}`

// writeFakeLighthouseScript drops a tiny shell script that prints
// canned Lighthouse JSON to stdout and exits 0 on first invocation.
// If failFirst is set, the first call exits 1 with the supplied stderr
// and the second succeeds — used to exercise the transient-retry path.
func writeFakeLighthouseScript(t *testing.T, failFirst bool, transientStderr string) (path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "called.txt")
	scriptPath := filepath.Join(dir, "fake-lighthouse.sh")

	body := "#!/bin/sh\n"
	if failFirst {
		// First call: touch the flag, write transient stderr, exit 1.
		// Second call: print canned JSON, exit 0.
		body += `if [ -f "` + flag + `" ]; then
  cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
  exit 0
fi
touch "` + flag + `"
echo "` + transientStderr + `" 1>&2
exit 1
`
	} else {
		body += `cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	}

	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(scriptPath, []byte(body), 0o755))
	return scriptPath
}

// newTestRunner builds a LocalRunner with a fake provider and an
// optional memory-shed shim. memMB < 0 disables the check by setting
// cfg.MemoryShedMB=0.
func newTestRunner(t *testing.T, lhBin string, memMB int, available int) (*LocalRunner, *fakeProvider) {
	t.Helper()
	provider := &fakeProvider{}
	r, err := NewLocalRunner(LocalRunnerConfig{
		LighthouseBin: lhBin,
		ChromiumBin:   "/usr/bin/chromium-fake",
		Provider:      provider,
		Bucket:        "test-bucket",
		MemoryShedMB:  memMB,
	})
	require.NoError(t, err)
	if memMB > 0 {
		r.readMemAvailableMB = func() (int, error) { return available, nil }
	}
	return r, provider
}

func TestNewLocalRunner_ValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  LocalRunnerConfig
	}{
		{"missing lighthouse bin", LocalRunnerConfig{ChromiumBin: "/c", Provider: &fakeProvider{}, Bucket: "b"}},
		{"missing chromium bin", LocalRunnerConfig{LighthouseBin: "/l", Provider: &fakeProvider{}, Bucket: "b"}},
		{"missing provider", LocalRunnerConfig{LighthouseBin: "/l", ChromiumBin: "/c", Bucket: "b"}},
		{"missing bucket", LocalRunnerConfig{LighthouseBin: "/l", ChromiumBin: "/c", Provider: &fakeProvider{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLocalRunner(tc.cfg)
			assert.Error(t, err)
		})
	}
}

func TestLocalRunner_MemoryShedReturnsSentinel(t *testing.T) {
	r, provider := newTestRunner(t, "/does-not-matter", 600, 200)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: time.Second,
	})
	require.ErrorIs(t, err, ErrMemoryShed)
	assert.Empty(t, provider.uploads, "no upload should occur when shed")
}

func TestLocalRunner_HappyPathUploadsAndReturnsKey(t *testing.T) {
	script := writeFakeLighthouseScript(t, false, "")
	r, provider := newTestRunner(t, script, 0, 0)

	t.Setenv("ARCHIVE_PATH_PREFIX", "")
	result, err := r.Run(context.Background(), AuditRequest{
		RunID:        7,
		JobID:        "job-abc",
		PageID:       42,
		SourceTaskID: "task-xyz",
		URL:          "https://example.com/page?token=secret",
		Profile:      ProfileMobile,
		Timeout:      5 * time.Second,
	})
	require.NoError(t, err)

	require.NotNil(t, result.PerformanceScore)
	assert.Equal(t, 91, *result.PerformanceScore)
	assert.Equal(t, "jobs/job-abc/tasks/task-xyz/lighthouse-mobile.json.gz", result.ReportKey)
	assert.Greater(t, result.Duration, time.Duration(0))

	require.Len(t, provider.uploads, 1)
	up := provider.uploads[0]
	assert.Equal(t, "test-bucket", up.bucket)
	assert.Equal(t, result.ReportKey, up.key)
	assert.Equal(t, "application/json", up.opts.ContentType)
	assert.Equal(t, "gzip", up.opts.ContentEncoding)
	assert.Equal(t, "7", up.opts.Metadata["run_id"])
}

// TestLocalRunner_DoesNotPassPresetFlagForMobile pins the contract
// against Lighthouse 12.x's CLI: --preset only accepts 'desktop',
// 'experimental', or 'perf'. Mobile is the implicit default and
// passing --preset=mobile fails argument validation before Chromium
// launches. The fix omits the flag entirely for mobile audits;
// regressing that would silently break every audit in production.
func TestLocalRunner_DoesNotPassPresetFlagForMobile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fake-lighthouse.sh")
	body := `#!/bin/sh
echo "$@" > "` + argsLog + `"
cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// #nosec G304 -- argsLog is t.TempDir-rooted, written above.
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.NotContains(t, string(logged), "--preset=mobile",
		"mobile is Lighthouse's implicit default; passing --preset=mobile is rejected")
	assert.NotContains(t, string(logged), "--preset=desktop",
		"desktop preset must not leak into a mobile audit")
}

// TestLocalRunner_PassesPresetForDesktop confirms desktop audits do
// flip --preset=desktop on. Currently dead-code-pathed since v1 only
// schedules mobile, but the runner-level code path exists and we want
// to catch a regression early when desktop ships in Phase 5.
func TestLocalRunner_PassesPresetForDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake unsupported on windows")
	}
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fake-lighthouse.sh")
	body := `#!/bin/sh
echo "$@" > "` + argsLog + `"
cat <<'JSON'
` + sampleLighthouseJSON + `
JSON
exit 0
`
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileDesktop, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// #nosec G304 -- argsLog is t.TempDir-rooted, written above.
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "--preset=desktop")
}

func TestLocalRunner_FallsBackToRunIDPathWhenNoTaskID(t *testing.T) {
	script := writeFakeLighthouseScript(t, false, "")
	r, provider := newTestRunner(t, script, 0, 0)

	t.Setenv("ARCHIVE_PATH_PREFIX", "")
	result, err := r.Run(context.Background(), AuditRequest{
		RunID: 99, JobID: "job-1", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
		// SourceTaskID intentionally empty.
	})
	require.NoError(t, err)
	assert.Equal(t, "jobs/job-1/runs/99/lighthouse-mobile.json.gz", result.ReportKey)
	require.Len(t, provider.uploads, 1)
}

func TestLocalRunner_RetriesOnTransientStderr(t *testing.T) {
	script := writeFakeLighthouseScript(t, true, "Inspector.targetCrashed: renderer died")
	r, provider := newTestRunner(t, script, 0, 0)

	result, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", SourceTaskID: "t", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 10 * time.Second,
	})
	require.NoError(t, err, "transient failure should retry and succeed")
	require.NotNil(t, result.PerformanceScore)
	require.Len(t, provider.uploads, 1)
}

func TestLocalRunner_NonTransientFailsImmediately(t *testing.T) {
	script := writeFakeLighthouseScript(t, true, "ENOENT: chromium not found")
	r, provider := newTestRunner(t, script, 0, 0)

	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 5 * time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ENOENT", "stderr tail should be in the error")
	assert.Empty(t, provider.uploads, "no upload on hard failure")
}

func TestLocalRunner_TimeoutCancelsAndKillsProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "sleep.sh")
	// #nosec G306 -- test fixture: the fake binary must be executable.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755))

	r, _ := newTestRunner(t, script, 0, 0)

	start := time.Now()
	_, err := r.Run(context.Background(), AuditRequest{
		RunID: 1, JobID: "j", URL: "https://example.com",
		Profile: ProfileMobile, Timeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"expected deadline exceeded, got %v", err)
	assert.Less(t, elapsed, 5*time.Second, "process tree must die promptly on timeout")
}

func TestIsTransientErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("Inspector.targetCrashed: renderer died"), true},
		{fmt.Errorf("Protocol error: connection lost"), true},
		{fmt.Errorf("Page crashed during eval"), true},
		{fmt.Errorf("Target closed"), true},
		{fmt.Errorf("WebSocket is not open"), true},
		{fmt.Errorf("ENOENT no such file"), false},
		{fmt.Errorf("invalid URL"), false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isTransientErr(tc.err), tc.err)
	}
}

func TestTailBuffer_KeepsLastBytes(t *testing.T) {
	t.Run("under cap stays as-is", func(t *testing.T) {
		buf := newTailBuffer(10)
		_, _ = buf.Write([]byte("hello"))
		assert.Equal(t, "hello", string(buf.tail()))
	})
	t.Run("at cap fills exactly", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("hello"))
		assert.Equal(t, "hello", string(buf.tail()))
	})
	t.Run("over cap keeps last cap bytes in chronological order", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("abc"))
		_, _ = buf.Write([]byte("defgh"))
		assert.Equal(t, "defgh", string(buf.tail()))
	})
	t.Run("single oversized write truncates to last cap", func(t *testing.T) {
		buf := newTailBuffer(4)
		_, _ = buf.Write([]byte("abcdefgh"))
		assert.Equal(t, "efgh", string(buf.tail()))
	})
	t.Run("wrap-around preserves order", func(t *testing.T) {
		buf := newTailBuffer(5)
		_, _ = buf.Write([]byte("abcde"))
		_, _ = buf.Write([]byte("fg"))
		assert.Equal(t, "cdefg", string(buf.tail()))
	})
}
