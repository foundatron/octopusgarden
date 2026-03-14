package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerRoutes(mux, NewStore())
	return httptest.NewServer(mux)
}

func TestHandleHealth(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHandleCreateItem(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid",
			body:       `{"name":"widget","description":"a widget"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "missing name",
			body:       `{"description":"no name"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid JSON",
			body:       `not json`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t)
			defer srv.Close()

			resp, err := http.Post(srv.URL+"/items", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestHandleGetItem(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Create an item first.
	body := `{"name":"thing","description":"a thing"}`
	resp, err := http.Post(srv.URL+"/items", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var created Item
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"found", created.ID, http.StatusOK},
		{"not found", "00000000-0000-0000-0000-000000000000", http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/items/" + tc.id)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestHandleListItems(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for i := range 3 {
		body, _ := json.Marshal(CreateItemRequest{Name: "item", Description: string(rune('A' + i))})
		resp, err := http.Post(srv.URL+"/items", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/items?limit=2&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var list ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 {
		t.Errorf("Total = %d, want 3", list.Total)
	}
	if len(list.Items) != 2 {
		t.Errorf("len(Items) = %d, want 2", len(list.Items))
	}
}

func TestHandleUpdateItem(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Create an item.
	resp, err := http.Post(srv.URL+"/items", "application/json", strings.NewReader(`{"name":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var created Item
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		id         string
		body       string
		wantStatus int
	}{
		{"update existing", created.ID, `{"name":"new"}`, http.StatusOK},
		{"not found", "00000000-0000-0000-0000-000000000000", `{"name":"x"}`, http.StatusNotFound},
		{"missing name", created.ID, `{"description":"no name"}`, http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/items/"+tc.id, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestHandleDeleteItem(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Create an item.
	resp, err := http.Post(srv.URL+"/items", "application/json", strings.NewReader(`{"name":"todelete"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var created Item
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"delete existing", created.ID, http.StatusNoContent},
		{"already deleted", created.ID, http.StatusNotFound},
		{"not found", "00000000-0000-0000-0000-000000000000", http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/items/"+tc.id, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}
