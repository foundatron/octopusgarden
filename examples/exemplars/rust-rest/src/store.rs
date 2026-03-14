use std::collections::HashMap;
use std::sync::{Arc, RwLock};

use chrono::Utc;
use uuid::Uuid;

use crate::models::{CreateItemRequest, Item, UpdateItemRequest};

#[derive(Default)]
struct StoreInner {
    items: HashMap<String, Item>,
    order: Vec<String>,
}

/// Store is a thread-safe in-memory item store with ordered iteration and O(1) lookup.
#[derive(Clone, Default)]
pub struct Store(Arc<RwLock<StoreInner>>);

impl Store {
    pub fn new() -> Self {
        Self::default()
    }

    /// Create adds a new item to the store and returns it.
    pub fn create(&self, req: CreateItemRequest) -> Item {
        let id = Uuid::new_v4().to_string();
        let item = Item {
            id: id.clone(),
            name: req.name,
            description: req.description.unwrap_or_default(),
            created_at: Utc::now(),
        };
        let mut inner = self.0.write().expect("lock poisoned");
        inner.items.insert(id.clone(), item.clone());
        inner.order.push(id);
        item
    }

    /// Get returns the item with the given ID, or None if not found.
    pub fn get(&self, id: &str) -> Option<Item> {
        let inner = self.0.read().expect("lock poisoned");
        inner.items.get(id).cloned()
    }

    /// List returns a paginated slice of items in insertion order and the total count.
    pub fn list(&self, limit: usize, offset: usize) -> (Vec<Item>, usize) {
        let inner = self.0.read().expect("lock poisoned");
        let total = inner.order.len();
        if offset >= total {
            return (Vec::new(), total);
        }
        let end = (offset + limit).min(total);
        let items = inner.order[offset..end]
            .iter()
            .filter_map(|id| inner.items.get(id).cloned())
            .collect();
        (items, total)
    }

    /// Update modifies an existing item, or returns None if not found.
    pub fn update(&self, id: &str, req: UpdateItemRequest) -> Option<Item> {
        let mut inner = self.0.write().expect("lock poisoned");
        let item = inner.items.get_mut(id)?;
        item.name = req.name;
        if let Some(desc) = req.description {
            item.description = desc;
        }
        Some(item.clone())
    }

    /// Delete removes an item from the store. Returns false if the item was not found.
    pub fn delete(&self, id: &str) -> bool {
        let mut inner = self.0.write().expect("lock poisoned");
        if inner.items.remove(id).is_none() {
            return false;
        }
        inner.order.retain(|oid| oid != id);
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_req(name: &str, desc: Option<&str>) -> CreateItemRequest {
        CreateItemRequest {
            name: name.to_string(),
            description: desc.map(str::to_string),
        }
    }

    #[test]
    fn test_create_and_get() {
        let store = Store::new();
        let item = store.create(make_req("widget", Some("a widget")));
        assert_eq!(item.name, "widget");
        assert_eq!(item.description, "a widget");

        let found = store.get(&item.id).unwrap();
        assert_eq!(found.id, item.id);
    }

    #[test]
    fn test_create_no_description() {
        let store = Store::new();
        let item = store.create(make_req("widget", None));
        assert_eq!(item.description, "");
    }

    #[test]
    fn test_get_not_found() {
        let store = Store::new();
        assert!(store.get("nonexistent").is_none());
    }

    #[test]
    fn test_list_pagination() {
        let store = Store::new();
        for i in 0..5 {
            store.create(make_req(&format!("item-{i}"), None));
        }

        let (items, total) = store.list(2, 0);
        assert_eq!(total, 5);
        assert_eq!(items.len(), 2);
        assert_eq!(items[0].name, "item-0");
        assert_eq!(items[1].name, "item-1");

        let (items, _) = store.list(2, 4);
        assert_eq!(items.len(), 1);

        let (items, _) = store.list(10, 10);
        assert!(items.is_empty());
    }

    #[test]
    fn test_update_preserves_description_when_none() {
        let store = Store::new();
        let item = store.create(make_req("original", Some("keep me")));

        let updated = store
            .update(
                &item.id,
                UpdateItemRequest {
                    name: "renamed".to_string(),
                    description: None,
                },
            )
            .unwrap();
        assert_eq!(updated.name, "renamed");
        assert_eq!(updated.description, "keep me");
    }

    #[test]
    fn test_update_replaces_description_when_provided() {
        let store = Store::new();
        let item = store.create(make_req("x", Some("old")));

        let updated = store
            .update(
                &item.id,
                UpdateItemRequest {
                    name: "x".to_string(),
                    description: Some("new".to_string()),
                },
            )
            .unwrap();
        assert_eq!(updated.description, "new");
    }

    #[test]
    fn test_update_not_found() {
        let store = Store::new();
        assert!(store
            .update(
                "nonexistent",
                UpdateItemRequest {
                    name: "x".to_string(),
                    description: None,
                }
            )
            .is_none());
    }

    #[test]
    fn test_delete() {
        let store = Store::new();
        let item = store.create(make_req("to-delete", None));

        assert!(store.delete(&item.id));
        assert!(!store.delete(&item.id)); // already gone
        assert!(store.get(&item.id).is_none());

        let (items, total) = store.list(10, 0);
        assert_eq!(total, 0);
        assert!(items.is_empty());
    }
}
