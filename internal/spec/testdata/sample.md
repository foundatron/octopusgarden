# Items REST API

A simple REST API for managing items with CRUD operations.

## Endpoints

### POST /items

Create a new item with a name and description.

### GET /items/{id}

Retrieve a single item by its ID.

### GET /items

List all items with optional pagination via `limit` and `offset` query parameters.

## Data Model

Each item has:

- `id` — auto-generated unique identifier
- `name` — required string
- `description` — optional string
- `created_at` — timestamp

## Validation

- `name` is required and must be non-empty
- Unknown fields should be ignored
