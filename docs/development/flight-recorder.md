# Flight Recorder

This document explains how to use the flight recorder for performance debugging.

## Enabling the Flight Recorder

The flight recorder can be enabled by setting the `FLIGHT_RECORDER_ENABLED`
environment variable to `true`. When enabled, the application will write a trace
file named `trace.out` to the root of the project.

## Accessing the Trace Data

The trace data can be accessed via the `/debug/fgtrace` endpoint. This endpoint
will return the `trace.out` file, which can be analyzed using `go tool trace`.

### Example Usage (Local Development)

1.  **Start the application with the flight recorder enabled:**

    ```bash
    FLIGHT_RECORDER_ENABLED=true go run cmd/app/main.go
    ```

2.  **Access the trace data:**

    ```bash
    curl -o trace.out http://localhost:8080/debug/fgtrace
    ```

3.  **Analyze the trace data:**
    ```bash
    go tool trace trace.out
    ```

### Production Usage (Fly.io)

1.  **Enable the flight recorder in your Fly.io environment:**

    ```bash
    fly secrets set FLIGHT_RECORDER_ENABLED=true
    ```

2.  **Download the trace data from your live application:**

    ```bash
    curl -o trace.out https://hover.fly.dev/debug/fgtrace
    ```

3.  **Analyze the trace data:**
    ```bash
    go tool trace trace.out
    ```

**Note:** The flight recorder should be used sparingly in production as it can
impact performance. Remember to disable it after collecting the needed trace
data:

```bash
fly secrets unset FLIGHT_RECORDER_ENABLED
```
