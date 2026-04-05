# Build stage
FROM golang:1.26.0-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/app/main.go

# Final stage
FROM alpine:3.19

# Create a non-root user
RUN adduser -D -g '' appuser

WORKDIR /app

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

# Copy binary from builder
COPY --from=builder /app/main .

# Copy static files
COPY --from=builder /app/dashboard.html .
COPY --from=builder /app/homepage.html .
COPY --from=builder /app/settings.html .
COPY --from=builder /app/welcome.html .
COPY --from=builder /app/invite-welcome.html .
COPY --from=builder /app/auth-modal.html .
COPY --from=builder /app/auth-callback.html .
COPY --from=builder /app/web/static ./web/static
COPY --from=builder /app/web/partials ./web/partials
COPY --from=builder /app/web/templates ./web/templates

# Copy migration files (required for reset-db endpoint)
COPY --from=builder /app/supabase/migrations ./supabase/migrations

# Add healthcheck
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Expose port
EXPOSE 8080

# Switch to non-root user
USER appuser

# Run the binary
CMD ["sh", "-c", "ulimit -n 65536 2>/dev/null || ulimit -n $(ulimit -Hn) 2>/dev/null; exec ./main"]
