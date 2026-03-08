# Kanban Board

A server-rendered Kanban board web application with a REST API backend. Users can create, view,
move, and delete cards across columns. The UI is HTML served by the application — no separate
frontend build step.

## Data Models

### Column

The board has three fixed columns in display order:

1. **To Do** — `todo`
1. **In Progress** — `in-progress`
1. **Done** — `done`

Columns are not user-configurable. The column id (slug) is used in the API and as `data-column`
attribute in the HTML.

### Card

- `id` — string, generated server-side on creation
- `title` — string, required, 1–200 characters
- `column` — string, one of `todo`, `in-progress`, `done`; default `todo`
- `created_at` — ISO 8601 timestamp, generated server-side

Data is stored in memory. Cards do not persist across server restarts. Cards are displayed in
creation order within each column.

## HTML UI

The application serves an HTML page at `GET /` that renders the Kanban board.

### Layout

- Page title: "Kanban Board" (in an `<h1>`)
- Three columns displayed side by side, each with a heading showing the column name
- Each column element has a `data-column` attribute set to the column slug
- Each card shows its title and has a `data-card-id` attribute set to the card id
- A form or input to create new cards with:
  - A text input with `data-testid="new-card-input"`
  - A submit button with `data-testid="add-card-button"`
- Each card has a delete button with `data-testid="delete-card"`
- Each card in "To Do" has a "Start" button with `data-testid="move-next"` to move it to "In
  Progress"
- Each card in "In Progress" has a "Done" button with `data-testid="move-next"` to move it to "Done"

### Behavior

- Submitting the new card form creates a card in the "To Do" column and refreshes the view
- Clicking "Start" or "Done" moves the card to the next column and refreshes the view
- Clicking delete removes the card and refreshes the view
- The page works without JavaScript (server-rendered with form submissions)

## REST API

All endpoints accept and return JSON. The API and HTML UI share the same data store.

### GET /api/cards

Returns all cards as a JSON array. Each card object has `id`, `title`, `column`, and `created_at`.
Empty board returns `[]`.

**Response:** `200 OK`

### POST /api/cards

Creates a new card.

**Request body:** `{"title": "Card title"}` — title is required.

**Response:** `201 Created` with the created card object including generated `id`, default `column`
of `todo`, and `created_at`.

**Errors:** `400 Bad Request` with `{"error": "..."}` if title is missing or empty.

### GET /api/cards/:id

Returns a single card by id.

**Response:** `200 OK` with the card object.

**Errors:** `404 Not Found` with `{"error": "..."}` if no card with that id exists.

### PUT /api/cards/:id

Updates a card. Accepts `title` and/or `column` in the request body.

**Response:** `200 OK` with the updated card object.

**Errors:**

- `404 Not Found` if no card with that id exists
- `400 Bad Request` if `column` is not one of `todo`, `in-progress`, `done`

### DELETE /api/cards/:id

Deletes a card.

**Response:** `204 No Content`

**Errors:** `404 Not Found` if no card with that id exists.
