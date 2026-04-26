package archive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ConfigFromEnv ---

func TestConfigFromEnv_BothUnset(t *testing.T) {
	t.Setenv("ARCHIVE_PROVIDER", "")
	t.Setenv("ARCHIVE_BUCKET", "")
	got := ConfigFromEnv()
	assert.Nil(t, got, "nil expected when both vars are unset")
}

func TestConfigFromEnv_ProviderUnset(t *testing.T) {
	t.Setenv("ARCHIVE_PROVIDER", "")
	t.Setenv("ARCHIVE_BUCKET", "my-bucket")
	got := ConfigFromEnv()
	assert.Nil(t, got, "nil expected when ARCHIVE_PROVIDER is unset")
}

func TestConfigFromEnv_BucketUnset(t *testing.T) {
	t.Setenv("ARCHIVE_PROVIDER", "r2")
	t.Setenv("ARCHIVE_BUCKET", "")
	got := ConfigFromEnv()
	assert.Nil(t, got, "nil expected when ARCHIVE_BUCKET is unset")
}

func TestConfigFromEnv_BothSet(t *testing.T) {
	t.Setenv("ARCHIVE_PROVIDER", "r2")
	t.Setenv("ARCHIVE_BUCKET", "my-archive")
	got := ConfigFromEnv()
	require.NotNil(t, got)
	assert.Equal(t, "r2", got.Provider)
	assert.Equal(t, "my-archive", got.Bucket)
}

func TestConfigFromEnv_CustomProvider(t *testing.T) {
	t.Setenv("ARCHIVE_PROVIDER", "s3")
	t.Setenv("ARCHIVE_BUCKET", "s3-bucket")
	got := ConfigFromEnv()
	require.NotNil(t, got)
	assert.Equal(t, "s3", got.Provider)
	assert.Equal(t, "s3-bucket", got.Bucket)
}

// --- DefaultConfig ---

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	assert.Equal(t, "r2", cfg.Provider)
	assert.Equal(t, "native-hover-archive", cfg.Bucket)
}

// --- ColdKey ---

func TestColdKey_DelegatesToTaskHTMLObjectPath(t *testing.T) {
	t.Setenv("ARCHIVE_PATH_PREFIX", "")
	assert.Equal(t, TaskHTMLObjectPath("j", "t"), ColdKey("j", "t"))
}

func TestColdKey_WithPrefix(t *testing.T) {
	t.Setenv("ARCHIVE_PATH_PREFIX", "42")
	assert.Equal(t, "42/jobs/j/tasks/t/page-content.html.gz", ColdKey("j", "t"))
}

func TestTaskHTMLObjectPath(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   string
	}{
		{
			name:   "no prefix",
			prefix: "",
			want:   "jobs/job-1/tasks/task-1/page-content.html.gz",
		},
		{
			name:   "pr number prefix",
			prefix: "347",
			want:   "347/jobs/job-1/tasks/task-1/page-content.html.gz",
		},
		{
			name:   "trims surrounding slashes",
			prefix: "/review-apps/347/",
			want:   "review-apps/347/jobs/job-1/tasks/task-1/page-content.html.gz",
		},
		{
			name:   "trims whitespace",
			prefix: "  347  ",
			want:   "347/jobs/job-1/tasks/task-1/page-content.html.gz",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARCHIVE_PATH_PREFIX", tc.prefix)
			got := TaskHTMLObjectPath("job-1", "task-1")
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
