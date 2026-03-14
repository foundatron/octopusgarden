use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// Item represents a resource in the store.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Item {
    pub id: String,
    pub name: String,
    pub description: String,
    pub created_at: DateTime<Utc>,
}

/// CreateItemRequest is the request body for creating an item.
#[derive(Debug, Deserialize)]
pub struct CreateItemRequest {
    pub name: String,
    pub description: Option<String>,
}

/// UpdateItemRequest is the request body for updating an item.
#[derive(Debug, Deserialize)]
pub struct UpdateItemRequest {
    pub name: String,
    pub description: Option<String>,
}

/// ErrorResponse is returned for all error responses.
#[derive(Debug, Serialize)]
pub struct ErrorResponse {
    pub error: String,
}

/// ListResponse wraps a paginated list of items.
#[derive(Debug, Serialize)]
pub struct ListResponse {
    pub items: Vec<Item>,
    pub total: usize,
    pub limit: usize,
    pub offset: usize,
}
