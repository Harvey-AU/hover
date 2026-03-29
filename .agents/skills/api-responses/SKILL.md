# API Response Format

All API endpoints follow this standardised response format.

## Success response

```json
{
  "status": "success",
  "data": {
    // Response data varies by endpoint
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0",
    "request_id": "req_123abc"
  }
}
```

## Error response

```json
{
  "status": "error",
  "error": {
    "code": "invalid_request",
    "message": "Invalid job configuration",
    "details": {
      "field": "max_pages",
      "issue": "Must be a positive integer"
    }
  },
  "meta": {
    "timestamp": "2023-05-18T12:34:56Z",
    "version": "1.0.0",
    "request_id": "req_123abc"
  }
}
```

## Standard error codes

- `invalid_request` - Malformed request or missing fields
- `authentication_required` - Missing/invalid auth
- `permission_denied` - Insufficient permissions
- `resource_not_found` - Resource doesn't exist
- `rate_limit_exceeded` - Too many requests
- `validation_failed` - Data fails validation
- `server_error` - Internal error

## HTTP status codes

- 200 Success, 201 Created
- 400 Bad Request, 401 Unauthorized, 403 Forbidden
- 404 Not Found, 409 Conflict, 422 Unprocessable
- 429 Rate Limited, 500 Internal Error

## Implementation

Use helpers in `internal/api/response.go` - never construct responses manually.
