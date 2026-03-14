package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errNotFound = errors.New("item not found")

// Store is an in-memory item store with ordered iteration and O(1) lookup.
type Store struct {
	mu    sync.RWMutex
	items map[string]*Item
	order []string // insertion-order IDs
}

// NewStore creates a new empty Store.
func NewStore() *Store {
	return &Store{
		items: make(map[string]*Item),
		order: make([]string, 0),
	}
}

// newID generates a UUID v4-style ID.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	// Set version (4) and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

// Create adds a new item to the store.
func (s *Store) Create(req CreateItemRequest) (Item, error) {
	id, err := newID()
	if err != nil {
		return Item{}, err
	}
	item := &Item{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = item
	s.order = append(s.order, id)
	return *item, nil
}

// Get returns the item with the given ID, or errNotFound.
func (s *Store) Get(id string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	if !ok {
		return Item{}, errNotFound
	}
	return *item, nil
}

// List returns a paginated slice of items in insertion order and the total count,
// both read under a single lock to avoid TOCTOU inconsistency.
func (s *Store) List(limit, offset int) ([]Item, int) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := len(s.order)
	if offset >= total {
		return []Item{}, total
	}
	end := min(offset+limit, total)
	result := make([]Item, 0, end-offset)
	for _, id := range s.order[offset:end] {
		result = append(result, *s.items[id])
	}
	return result, total
}

// Update modifies an existing item, or returns errNotFound.
func (s *Store) Update(id string, req UpdateItemRequest) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return Item{}, errNotFound
	}
	item.Name = req.Name
	item.Description = req.Description
	return *item, nil
}

// Delete removes an item from the store, or returns errNotFound.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return errNotFound
	}
	delete(s.items, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}
