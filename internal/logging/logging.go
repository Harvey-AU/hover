// Package logging provides a unified slog + Sentry logging interface.
//
// Every log call automatically includes a component tag for structured
// monitoring. Error-level calls auto-capture to Sentry via the
// sentry-go/slog handler. Use NoCapture in the context to suppress
// capture for expected errors.
package logging

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	sentryslog "github.com/getsentry/sentry-go/slog"
)

// uuidPattern matches standard UUIDs in error messages.
var uuidPattern = regexp.MustCompile(
	`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
)

// numberPattern matches bare integers (3+ digits) in messages.
var numberPattern = regexp.MustCompile(`\b\d{3,}\b`)

// contextKey is an unexported type for context keys in this package.
type contextKey int

const (
	noCaptureKey contextKey = iota
)

// NoCapture returns a derived context that suppresses Sentry capture for any
// ErrorContext call made with it. The suppression persists for the lifetime of
// the derived context — construct it inline so the scope is limited to one call:
//
//	log.ErrorContext(logging.NoCapture(ctx), "expected 404", "url", url, "error", err)
//
// Do not store the derived context and reuse it, or all subsequent error log
// calls on that context will also be silently dropped from Sentry.
func NoCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCaptureKey, true)
}

func isNoCapture(ctx context.Context) bool {
	v, _ := ctx.Value(noCaptureKey).(bool)
	return v
}

// BeforeSend is a sentry.EventProcessor that normalises event messages
// by replacing UUIDs and bare numbers with placeholders. Install on
// sentry.Init to prevent dynamic values from fragmenting issue grouping.
func BeforeSend(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
	// Drop events explicitly marked as no-capture via context.
	if v, ok := event.Extra["no_capture"]; ok {
		if suppress, _ := v.(bool); suppress {
			return nil // Drop the event.
		}
	}

	event.Message = normaliseMessage(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = normaliseMessage(event.Exception[i].Value)
	}
	return event
}

func normaliseMessage(msg string) string {
	msg = uuidPattern.ReplaceAllString(msg, "<uuid>")
	msg = numberPattern.ReplaceAllString(msg, "<n>")
	return msg
}

// Logger is a component-scoped structured logger. It rebuilds the underlying
// slog.Logger at emit time so it always picks up the current slog.Default()
// (i.e., the fanout handler installed by Setup after sentry.Init).
type Logger struct {
	component string
	attrs     []any
}

// Component creates a component-scoped logger. The component name appears
// as a structured field, a Sentry tag, and a human-readable prefix.
func Component(name string) *Logger {
	return &Logger{component: name}
}

// With returns a new Logger that includes the given attributes on every
// subsequent log call. Useful for adding request-scoped fields.
func (l *Logger) With(args ...any) *Logger {
	newAttrs := make([]any, len(l.attrs)+len(args))
	copy(newAttrs, l.attrs)
	copy(newAttrs[len(l.attrs):], args)
	return &Logger{component: l.component, attrs: newAttrs}
}

// base builds a concrete *slog.Logger from the current slog.Default().
// Called at emit time so Setup's SetDefault is always picked up.
func (l *Logger) base() *slog.Logger {
	b := slog.Default().With("component", l.component)
	if len(l.attrs) > 0 {
		b = b.With(l.attrs...)
	}
	return b
}

// Debug logs at debug level (no Sentry capture).
func (l *Logger) Debug(msg string, args ...any) {
	l.base().Debug(l.prefix(msg), args...)
}

// DebugContext logs at debug level with a context.
func (l *Logger) DebugContext(ctx context.Context, msg string, args ...any) {
	l.base().DebugContext(ctx, l.prefix(msg), args...)
}

// Info logs at info level (no Sentry capture).
func (l *Logger) Info(msg string, args ...any) {
	l.base().Info(l.prefix(msg), args...)
}

// InfoContext logs at info level with a context.
func (l *Logger) InfoContext(ctx context.Context, msg string, args ...any) {
	l.base().InfoContext(ctx, l.prefix(msg), args...)
}

// Warn logs at warn level (no Sentry capture by default).
func (l *Logger) Warn(msg string, args ...any) {
	l.base().Warn(l.prefix(msg), args...)
}

// WarnContext logs at warn level with a context.
func (l *Logger) WarnContext(ctx context.Context, msg string, args ...any) {
	l.base().WarnContext(ctx, l.prefix(msg), args...)
}

// Error logs at error level. Auto-captured to Sentry via the handler.
// Tags and fingerprint are injected automatically.
func (l *Logger) Error(msg string, args ...any) {
	l.base().Error(l.prefix(msg), l.withSentryAttrs(msg, args)...)
}

// ErrorContext logs at error level with a context. Use NoCapture(ctx)
// to suppress Sentry capture for expected errors.
func (l *Logger) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.base().ErrorContext(ctx, l.prefix(msg), l.withSentryAttrs(msg, args)...)
}

// Fatal logs at error level, captures to Sentry, flushes, and exits.
func (l *Logger) Fatal(msg string, args ...any) {
	l.base().Error(l.prefix(msg), l.withSentryAttrs(msg, args)...)
	sentry.Flush(5 * time.Second)
	os.Exit(1)
}

func (l *Logger) prefix(msg string) string {
	return "[" + l.component + "] " + msg
}

// withSentryAttrs appends Sentry-specific attributes (tags group and
// fingerprint) to the args slice. Only added for error-level calls
// where the Sentry handler will process them.
func (l *Logger) withSentryAttrs(msg string, args []any) []any {
	return append(args,
		slog.Group("tags",
			slog.String("component", l.component),
		),
		slog.Any("fingerprint", []string{l.component, normaliseMessage(msg)}),
	)
}

// fanoutHandler writes log records to multiple handlers.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var firstErr error
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, record.Level) {
			if err := hh.Handle(ctx, record.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: handlers}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithGroup(name)
	}
	return &fanoutHandler{handlers: handlers}
}

// Setup configures the global slog default with both stdout output and
// Sentry capture. Call this after sentry.Init() during application startup.
//
// In development, logs are human-readable text. In production, JSON.
// Error-level logs are auto-captured to Sentry with component tags
// and static fingerprints.
func Setup(level slog.Level, env string) {
	var outputHandler slog.Handler

	opts := &slog.HandlerOptions{Level: level}

	if env == "development" {
		outputHandler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		outputHandler = slog.NewJSONHandler(os.Stdout, opts)
	}

	sentryHandler := sentryslog.Option{
		EventLevel: []slog.Level{slog.LevelError},
		Converter:  componentConverter,
		AttrFromContext: []func(ctx context.Context) []slog.Attr{
			noCaptureAttr,
		},
	}.NewSentryHandler(context.Background())

	slog.SetDefault(slog.New(&fanoutHandler{
		handlers: []slog.Handler{outputHandler, sentryHandler},
	}))
}

// componentConverter maps slog records to Sentry events with proper
// component tags and fingerprinting.
func componentConverter(addSource bool, replaceAttr func([]string, slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record, hub *sentry.Hub) *sentry.Event {
	event := &sentry.Event{
		Level:   sentryLevel(record.Level),
		Message: record.Message,
		Tags:    make(map[string]string),
		Extra:   make(map[string]interface{}),
	}

	// Process logger-level attrs (from With()).
	for _, a := range loggerAttr {
		processAttr(event, a)
	}

	// Process record-level attrs.
	record.Attrs(func(a slog.Attr) bool {
		processAttr(event, a)
		return true
	})

	// Ensure component is always in tags.
	if _, ok := event.Tags["component"]; !ok {
		event.Tags["component"] = "unknown"
	}

	// Default fingerprint: component + normalised message.
	if len(event.Fingerprint) == 0 {
		event.Fingerprint = []string{
			event.Tags["component"],
			normaliseMessage(record.Message),
		}
	}

	return event
}

// processAttr maps a single slog.Attr into the Sentry event.
func processAttr(event *sentry.Event, a slog.Attr) {
	switch a.Key {
	case "fingerprint":
		if fp, ok := a.Value.Any().([]string); ok {
			event.Fingerprint = fp
		}

	case "tags":
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				event.Tags[ga.Key] = ga.Value.String()
			}
		}

	case "error", "err":
		if err, ok := a.Value.Any().(error); ok {
			event.SetException(err, -1)
		} else {
			event.Extra[a.Key] = a.Value.String()
		}

	case "no_capture":
		// Passed through to Extra so BeforeSend can drop the event.
		if b, ok := a.Value.Any().(bool); ok && b {
			event.Extra["no_capture"] = true
		}

	default:
		// All non-tag string attributes go to Extra, not Tags.
		// Only attributes inside the explicit "tags" slog.Group are low-cardinality
		// enough to be Sentry tags. Everything else (job IDs, domains, org IDs, etc.)
		// would fragment the tag index.
		switch a.Value.Kind() {
		case slog.KindGroup:
			for _, ga := range a.Value.Group() {
				event.Extra[a.Key+"."+ga.Key] = ga.Value.Any()
			}
		default:
			event.Extra[a.Key] = a.Value.Any()
		}
	}
}

// noCaptureAttr extracts the no-capture flag from context so the Sentry
// handler can see it as an attribute.
func noCaptureAttr(ctx context.Context) []slog.Attr {
	if isNoCapture(ctx) {
		return []slog.Attr{slog.Bool("no_capture", true)}
	}
	return nil
}

func sentryLevel(l slog.Level) sentry.Level {
	switch {
	case l >= slog.LevelError:
		return sentry.LevelError
	case l >= slog.LevelWarn:
		return sentry.LevelWarning
	case l >= slog.LevelInfo:
		return sentry.LevelInfo
	default:
		return sentry.LevelDebug
	}
}

// ParseLevel converts a string level name to slog.Level.
// Supports zerolog-compatible names for backwards compatibility.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug", "trace":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
