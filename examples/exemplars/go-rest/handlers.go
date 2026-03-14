package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an ErrorResponse with the given status and message.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// registerRoutes wires all routes onto mux.
func registerRoutes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /items", handleCreateItem(store))
	mux.HandleFunc("GET /items", handleListItems(store))
	mux.HandleFunc("GET /items/{id}", handleGetItem(store))
	mux.HandleFunc("PUT /items/{id}", handleUpdateItem(store))
	mux.HandleFunc("DELETE /items/{id}", handleDeleteItem(store))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleCreateItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CreateItemRequest
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		item, err := store.Create(req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create item")
			return
		}
		w.Header().Set("Location", "/items/"+item.ID)
		writeJSON(w, http.StatusCreated, item)
	}
}

func handleListItems(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}
		items, total := store.List(limit, offset)
		writeJSON(w, http.StatusOK, ListResponse{
			Items:  items,
			Total:  total,
			Limit:  limit,
			Offset: offset,
		})
	}
}

func handleGetItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := store.Get(id)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get item")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func handleUpdateItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req UpdateItemRequest
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		item, err := store.Update(id, req)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update item")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func handleDeleteItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := store.Delete(id)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete item")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
