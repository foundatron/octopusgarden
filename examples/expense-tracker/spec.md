# Expense Tracker API

A multi-user expense tracking API with categories, date-range filtering, and per-category spending
summaries. Users register to get an API key, create categories to organize expenses, and record
expenses with amounts and dates.

## Data Models

### User

- `id` — UUID, generated server-side on registration
- `username` — string, required, unique, 3–50 characters
- `api_key` — UUID, generated server-side on registration

### Category

- `id` — UUID, generated server-side on creation
- `user_id` — UUID, the owning user
- `name` — string, required, 1–100 characters, unique per user
- `created_at` — ISO 8601 timestamp, generated server-side on creation

### Expense

- `id` — UUID, generated server-side on creation
- `user_id` — UUID, the owning user
- `category_id` — UUID, optional, must reference an existing category owned by the same user
- `amount` — number, required, must be greater than zero (supports decimals, e.g. 9.99)
- `description` — string, required, 1–500 characters
- `date` — string, required, format YYYY-MM-DD
- `created_at` — ISO 8601 timestamp, generated server-side on creation

## Authentication

All `/categories` and `/expenses` endpoints require an `Authorization: Bearer {api_key}` header
where `{api_key}` is the user's API key returned from registration. Missing or invalid API key
returns `401 Unauthorized` with `{"error": "..."}`.

## Endpoints

### POST /users

Register a new user.

- Request body: `{"username": "..."}`
- Response: `201 Created` with `{"id": "...", "username": "...", "api_key": "..."}`
- Validation: return `400 Bad Request` with `{"error": "..."}` if username is missing, empty,
  shorter than 3 characters, or exceeds 50 characters
- Duplicate: return `409 Conflict` with `{"error": "..."}` if username is already taken

### POST /categories

Create a new category for the authenticated user.

- Request body: `{"name": "..."}`
- Response: `201 Created` with the full category JSON including `id`, `user_id`, and `created_at`
- Validation: return `400 Bad Request` with `{"error": "..."}` if name is missing, empty, or exceeds
  100 characters
- Duplicate: return `409 Conflict` with `{"error": "..."}` if the user already has a category with
  the same name (case-sensitive)

### GET /categories

List the authenticated user's categories.

- Response: `200 OK` with `{"categories": [...]}`
- Only returns categories belonging to the authenticated user

### DELETE /categories/{id}

Delete a category. Expenses referencing this category retain their `category_id` value (it becomes a
dangling reference) — they are not deleted or modified.

- Response: `204 No Content`
- If not found or owned by a different user: `404 Not Found`

### POST /expenses

Create a new expense for the authenticated user.

- Request body:
  `{"amount": 42.50, "description": "...", "date": "2025-01-15", "category_id": "..."}` where
  `category_id` is optional
- Response: `201 Created` with the full expense JSON including `id`, `user_id`, `category_id` (null
  if not provided), and `created_at`
- Validation: return `400 Bad Request` with `{"error": "..."}` if:
  - `amount` is missing, zero, or negative
  - `description` is missing, empty, or exceeds 500 characters
  - `date` is missing or not in YYYY-MM-DD format
  - `category_id` is provided but does not reference an existing category owned by the same user

### GET /expenses/{id}

Retrieve an expense by ID.

- Response: `200 OK` with the expense JSON
- If not found or owned by a different user: `404 Not Found`

### PUT /expenses/{id}

Update an expense.

- Request body: `{"amount": ..., "description": "...", "date": "...", "category_id": "..."}` — all
  fields optional, only provided fields are updated
- To clear `category_id`, send `"category_id": null`
- Response: `200 OK` with the updated expense JSON
- If not found or owned by a different user: `404 Not Found`
- Validation: same rules as creation for any provided fields

### DELETE /expenses/{id}

Delete an expense.

- Response: `204 No Content`
- If not found or owned by a different user: `404 Not Found`

### GET /expenses

List the authenticated user's expenses with optional filtering and pagination.

- Query parameters:
  - `category_id` — UUID filter, optional; return only expenses with this category
  - `date_from` — YYYY-MM-DD, optional; return only expenses on or after this date
  - `date_to` — YYYY-MM-DD, optional; return only expenses on or before this date
  - `limit` — integer, default 20, max 100
  - `offset` — integer, default 0
- Response: `200 OK` with `{"expenses": [...], "total": N}` where `total` is the count of matching
  expenses (respecting all filters)
- Only returns expenses belonging to the authenticated user

### GET /expenses/summary

Return per-category spending totals for the authenticated user.

- Query parameters:
  - `date_from` — YYYY-MM-DD, optional; include only expenses on or after this date
  - `date_to` — YYYY-MM-DD, optional; include only expenses on or before this date
- Response: `200 OK` with the following structure:

```json
{
  "categories": [
    {"category_id": "...", "category_name": "...", "total": 150.00, "count": 3},
    {"category_id": null, "category_name": "Uncategorized", "total": 25.50, "count": 1}
  ],
  "grand_total": 175.50
}
```

- Categories with zero expenses in the date range are omitted
- Expenses without a `category_id` are grouped under `null` / `"Uncategorized"`
- `total` is the sum of `amount` for that category; `count` is the number of expenses
- Only includes the authenticated user's expenses

## Ownership

Users can only access their own categories and expenses. Attempting to access another user's
resource returns `404 Not Found` (not `403 Forbidden`) to avoid leaking the existence of other
users' resources.

## Requirements

- In-memory storage (no database required)
- Listen on port 8080
- Any programming language
- JSON content type for all responses
