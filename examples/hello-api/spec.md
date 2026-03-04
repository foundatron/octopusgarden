# Items REST API

A simple CRUD REST API for managing items.

## Data Model

An **Item** has the following fields:

- `id` — UUID, generated server-side on creation
- `name` — string, required, 1–100 characters
- `description` — string, optional
- `created_at` — ISO 8601 timestamp, generated server-side on creation

## Endpoints

### POST /items

Create a new item.

- Request body: `{"name": "...", "description": "..."}`
- Response: `201 Created` with the full item JSON including `id` and `created_at`
- Validation: return `400 Bad Request` with `{"error": "..."}` if `name` is missing or empty or
  exceeds 100 characters

### GET /items/{id}

Retrieve an item by ID.

- Response: `200 OK` with the item JSON
- If not found: `404 Not Found`

### PUT /items/{id}

Update an existing item.

- Request body: `{"name": "...", "description": "..."}`
- Response: `200 OK` with the updated item JSON
- If not found: `404 Not Found`
- Validation: same rules as POST

### DELETE /items/{id}

Delete an item.

- Response: `204 No Content`
- If not found: `404 Not Found`

### GET /items

List items with pagination.

- Query parameters:
  - `limit` — integer, default 20, max 100
  - `offset` — integer, default 0
- Response: `200 OK` with `{"items": [...], "total": N}` where `total` is the total count of all
  items

## Requirements

- In-memory storage (no database required)
- Listen on port 8080
- Any programming language
- JSON content type for all responses
