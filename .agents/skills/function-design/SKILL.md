# Function Design

This project follows focused, testable function design established through
systematic refactoring.

## Guidelines

- **Under 50 lines** where possible
- **Single responsibility** - each function has one clear purpose
- **Descriptive names** - `parseTaskQueryParams()`, `validateJobAccess()`,
  `buildTaskQuery()`

## Extract + Test + Commit Pattern

When encountering large functions:

1. **Analyse** - Identify distinct responsibilities (auth, validation,
   processing, formatting)
2. **Extract** - Pull out single-responsibility functions with idiomatic Go
   error patterns
3. **Test** - Write table-driven tests covering edge cases and errors
4. **Commit** - Commit extraction and tests together, verify build passes

## Proven results

- `getJobTasks`: 216 → 56 lines (74% reduction)
- `CreateJob`: 232 → 42 lines (82% reduction)
- `WarmURL`: 377 → 68 lines (82% reduction)

## Error handling

```go
// Return simple errors, wrap with context
func validateJobAccess(ctx context.Context, jobID string) error {
    if jobID == "" {
        return fmt.Errorf("job ID required")
    }
    // ...
    return fmt.Errorf("validating job access: %w", err)
}
```

## Testing approach

- Table-driven tests for each extracted function
- Cover edge cases, error conditions, parameter validation
- Use sqlmock for DB, context for cancellation
- Test isolation and integration
