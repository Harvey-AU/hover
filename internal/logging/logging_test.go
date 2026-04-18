package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// capturedEvent holds a Sentry event captured during tests.
var capturedEvents []*sentry.Event

func setupTestSentry(t *testing.T) {
	t.Helper()
	capturedEvents = nil

	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "",
		Transport: &transportMock{},
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			event = BeforeSend(event, hint)
			if event != nil {
				capturedEvents = append(capturedEvents, event)
			}
			return event
		},
	})
	if err != nil {
		t.Fatalf("sentry.Init failed: %v", err)
	}
}

// transportMock is a no-op Sentry transport for testing.
type transportMock struct{}

func (t *transportMock) Flush(_ time.Duration) bool              { return true }
func (t *transportMock) FlushWithContext(_ context.Context) bool { return true }
func (t *transportMock) Configure(_ sentry.ClientOptions)        {}
func (t *transportMock) SendEvent(_ *sentry.Event)               {}
func (t *transportMock) Close()                                  {}

func TestComponent(t *testing.T) {
	l := Component("worker")
	if l.component != "worker" {
		t.Errorf("expected component 'worker', got %q", l.component)
	}
}

func TestPrefix(t *testing.T) {
	l := Component("api")
	got := l.prefix("request failed")
	if got != "[api] request failed" {
		t.Errorf("expected '[api] request failed', got %q", got)
	}
}

func TestWith(t *testing.T) {
	l := Component("db").With("host", "localhost")
	if l.component != "db" {
		t.Errorf("With should preserve component, got %q", l.component)
	}
}

func TestNormaliseMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "uuid",
			in:   "failed for job 550e8400-e29b-41d4-a716-446655440000: timeout",
			want: "failed for job <uuid>: timeout",
		},
		{
			name: "numbers",
			in:   "found 1234 stuck jobs across 567 workers",
			want: "found <n> stuck jobs across <n> workers",
		},
		{
			name: "small numbers preserved",
			in:   "retry attempt 2 of 3",
			want: "retry attempt 2 of 3",
		},
		{
			name: "mixed",
			in:   "org 550e8400-e29b-41d4-a716-446655440000 has 9999 pages",
			want: "org <uuid> has <n> pages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normaliseMessage(tt.in)
			if got != tt.want {
				t.Errorf("normaliseMessage(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBeforeSendNormalises(t *testing.T) {
	event := &sentry.Event{
		Message: "dashboard stats query failed for org 550e8400-e29b-41d4-a716-446655440000",
		Exception: []sentry.Exception{
			{Value: "batch poison pill after 1234 failures"},
		},
	}

	result := BeforeSend(event, nil)
	if result == nil {
		t.Fatal("BeforeSend should not drop events without no_capture")
	}

	if strings.Contains(result.Message, "550e8400") {
		t.Error("BeforeSend should strip UUIDs from message")
	}
	if strings.Contains(result.Exception[0].Value, "1234") {
		t.Error("BeforeSend should strip numbers from exception values")
	}
}

func TestBeforeSendDropsNoCapture(t *testing.T) {
	event := &sentry.Event{
		Message: "expected error",
		Extra:   map[string]interface{}{"no_capture": true},
	}

	result := BeforeSend(event, nil)
	if result != nil {
		t.Error("BeforeSend should return nil for no_capture events")
	}
}

func TestNoCapture(t *testing.T) {
	ctx := context.Background()
	if isNoCapture(ctx) {
		t.Error("plain context should not be no_capture")
	}

	ctx = NoCapture(ctx)
	if !isNoCapture(ctx) {
		t.Error("NoCapture context should be detected")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"trace", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"fatal", slog.LevelError},
		{"panic", slog.LevelError},
		{"unknown", slog.LevelWarn},
		{"DEBUG", slog.LevelDebug},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := ParseLevel(tt.in)
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSentryLevel(t *testing.T) {
	if sentryLevel(slog.LevelError) != sentry.LevelError {
		t.Error("error should map to sentry error")
	}
	if sentryLevel(slog.LevelWarn) != sentry.LevelWarning {
		t.Error("warn should map to sentry warning")
	}
	if sentryLevel(slog.LevelInfo) != sentry.LevelInfo {
		t.Error("info should map to sentry info")
	}
	if sentryLevel(slog.LevelDebug) != sentry.LevelDebug {
		t.Error("debug should map to sentry debug")
	}
}

func TestLoggerOutputJSON(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))

	l := Component("test")
	l.Info("hello", "key", "val")

	var m map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}

	if m["component"] != "test" {
		t.Errorf("expected component 'test', got %v", m["component"])
	}
	if msg, ok := m["msg"].(string); !ok || msg != "[test] hello" {
		t.Errorf("expected '[test] hello', got %v", m["msg"])
	}
	if m["key"] != "val" {
		t.Errorf("expected key 'val', got %v", m["key"])
	}
}

func TestComponentConverterTags(t *testing.T) {
	record := slog.NewRecord(time.Now(), slog.LevelError, "[worker] task failed", 0)

	// Simulate the attrs added by withSentryAttrs.
	record.AddAttrs(
		slog.Group("tags",
			slog.String("component", "worker"),
		),
		slog.Any("fingerprint", []string{"worker", "task failed"}),
		slog.String("job_id", "abc-123"),
		slog.Any("error", errors.New("connection refused")),
	)

	event := componentConverter(false, nil, nil, nil, &record, nil)

	if event.Tags["component"] != "worker" {
		t.Errorf("expected tag component=worker, got %q", event.Tags["component"])
	}
	// job_id is high-cardinality — should go to Extra, not Tags.
	if v, ok := event.Extra["job_id"]; !ok || v != "abc-123" {
		t.Errorf("expected extra job_id=abc-123, got %v", event.Extra["job_id"])
	}
	if _, inTags := event.Tags["job_id"]; inTags {
		t.Error("job_id should not be in Tags (high-cardinality)")
	}
	if len(event.Fingerprint) != 2 || event.Fingerprint[0] != "worker" || event.Fingerprint[1] != "task failed" {
		t.Errorf("unexpected fingerprint: %v", event.Fingerprint)
	}
	if len(event.Exception) == 0 {
		t.Error("expected error to be set as exception")
	}
}

func TestFanoutHandler(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	h1 := slog.NewJSONHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelDebug})
	h2 := slog.NewJSONHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelDebug})

	fan := &fanoutHandler{handlers: []slog.Handler{h1, h2}}
	logger := slog.New(fan)
	logger.Info("test")

	if buf1.Len() == 0 {
		t.Error("handler 1 should have received the log")
	}
	if buf2.Len() == 0 {
		t.Error("handler 2 should have received the log")
	}
}

func TestFanoutHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fan := &fanoutHandler{handlers: []slog.Handler{h}}

	logger := slog.New(fan.WithAttrs([]slog.Attr{slog.String("env", "test")}))
	logger.Info("hello")

	var m map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["env"] != "test" {
		t.Errorf("expected env=test in output, got %v", m)
	}
}

func TestFanoutHandlerWithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fan := &fanoutHandler{handlers: []slog.Handler{h}}

	logger := slog.New(fan.WithGroup("req").WithAttrs([]slog.Attr{slog.String("id", "123")}))
	logger.Info("hello")

	var m map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if req, ok := m["req"].(map[string]interface{}); !ok || req["id"] != "123" {
		t.Errorf("expected grouped attr req.id=123, got %v", m)
	}
}
