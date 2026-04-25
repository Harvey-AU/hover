package archive

import "testing"

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
