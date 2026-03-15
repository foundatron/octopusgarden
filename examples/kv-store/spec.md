# Key-Value Store API

A REST API for a key-value store with TTL support and bulk operations.

## Data Model

A **Record** has:

- `key` -- string, required, 1-256 characters, alphanumeric plus hyphens and underscores
- `value` -- any valid JSON value (string, number, object, array, boolean, null)
- `ttl` -- integer, optional, seconds until expiration (0 or absent means no expiration)
- `created_at` -- ISO 8601 timestamp, server-generated
- `expires_at` -- ISO 8601 timestamp, server-generated from ttl (null if no expiration)

## Endpoints

### PUT /kv/{key}

Create or overwrite a record.

- Request body: `{"value": ..., "ttl": 60}`
- Response: `201 Created` with the full record JSON
- Validation: return `400 Bad Request` with `{"error": "..."}` if key is invalid (empty, too long,
  or contains disallowed characters)
- If key already exists, overwrite it (upsert semantics)

### GET /kv/{key}

Retrieve a record by key.

- Response: `200 OK` with the record JSON
- If not found or expired: `404 Not Found`

### DELETE /kv/{key}

Delete a record.

- Response: `204 No Content`
- If not found: `404 Not Found`

### GET /kv

List all non-expired keys.

- Query parameters:
  - `prefix` -- string, optional, filter keys by prefix
  - `limit` -- integer, default 100, max 1000
- Response: `200 OK` with `{"keys": ["key1", "key2", ...], "count": N}`

### POST /kv/\_bulk

Bulk set multiple records.

- Request body: `{"records": [{"key": "...", "value": ..., "ttl": 60}, ...]}`
- Response: `200 OK` with `{"created": N, "errors": [{"key": "...", "error": "..."}]}`
- Partial success allowed: valid records are stored even if some fail validation
- Maximum 100 records per request; return `400` if exceeded

### GET /health

Health check endpoint.

- Response: `200 OK` with `{"status": "ok"}`

## Requirements

- In-memory storage (no database required)
- Listen on port 8080
- Any programming language
- JSON content type for all responses
- Expired records must not be returned by GET or list endpoints
