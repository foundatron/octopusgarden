# Todo App API

A multi-user todo list API with API key authentication and per-user data isolation.

## Data Models

### User

- `id` — UUID, generated server-side on registration
- `username` — string, required, unique, 3–50 characters
- `api_key` — UUID, generated server-side on registration

### Todo

- `id` — UUID, generated server-side on creation
- `user_id` — UUID, the owning user
- `title` — string, required, 1–200 characters
- `completed` — boolean, default false
- `created_at` — ISO 8601 timestamp, generated server-side on creation

## Authentication

All `/todos` endpoints require an `Authorization: Bearer {api_key}` header where `{api_key}` is the
user's API key returned from registration. Missing or invalid API key returns `401 Unauthorized`
with `{"error": "..."}`.

## Endpoints

### POST /users

Register a new user.

- Request body: `{"username": "..."}`
- Response: `201 Created` with `{"id": "...", "username": "...", "api_key": "..."}`
- Validation: return `400 Bad Request` with `{"error": "..."}` if username is missing, empty,
  shorter than 3 characters, or exceeds 50 characters
- Duplicate: return `409 Conflict` with `{"error": "..."}` if username is already taken

### GET /todos

List the authenticated user's todos with optional filtering and pagination.

- Query parameters:
  - `completed` — boolean filter (`true` or `false`), optional
  - `limit` — integer, default 20, max 100
  - `offset` — integer, default 0
- Response: `200 OK` with `{"todos": [...], "total": N}` where `total` is the count of matching
  todos (respecting the `completed` filter if set)
- Only returns todos belonging to the authenticated user

### POST /todos

Create a new todo for the authenticated user.

- Request body: `{"title": "..."}`
- Response: `201 Created` with the full todo JSON including `id`, `user_id`, `completed`, and
  `created_at`
- Validation: return `400 Bad Request` with `{"error": "..."}` if title is missing, empty, or
  exceeds 200 characters

### GET /todos/{id}

Retrieve a todo by ID.

- Response: `200 OK` with the todo JSON
- If not found or owned by a different user: `404 Not Found`

### PUT /todos/{id}

Update a todo.

- Request body: `{"title": "...", "completed": true/false}` — both fields optional, only provided
  fields are updated
- Response: `200 OK` with the updated todo JSON
- If not found or owned by a different user: `404 Not Found`
- Validation: return `400 Bad Request` with `{"error": "..."}` if title is provided but empty or
  exceeds 200 characters

### DELETE /todos/{id}

Delete a todo.

- Response: `204 No Content`
- If not found or owned by a different user: `404 Not Found`

## Ownership

Users can only access their own todos. Attempting to access another user's todo returns
`404 Not Found` (not `403 Forbidden`) to avoid leaking the existence of other users' resources.

## Requirements

- In-memory storage (no database required)
- Listen on port 8080
- JSON content type for all responses
