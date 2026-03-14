package main

import "time"

// Item represents a resource in the store.
type Item struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateItemRequest is the request body for creating an item.
type CreateItemRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UpdateItemRequest is the request body for updating an item.
type UpdateItemRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ErrorResponse is returned for all error responses.
type ErrorResponse struct {
	Error string `json:"error"`
}

// ListResponse wraps a paginated list of items.
type ListResponse struct {
	Items  []Item `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}
