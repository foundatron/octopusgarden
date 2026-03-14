use axum::{
    extract::{Path, Query, State},
    http::{HeaderValue, StatusCode},
    response::{IntoResponse, Response},
    Json,
};
use serde::Deserialize;

use crate::models::{CreateItemRequest, ErrorResponse, ListResponse, UpdateItemRequest};
use crate::store::Store;

pub async fn health() -> impl IntoResponse {
    Json(serde_json::json!({"status": "ok"}))
}

fn not_found() -> Response {
    (
        StatusCode::NOT_FOUND,
        Json(ErrorResponse {
            error: "item not found".to_string(),
        }),
    )
        .into_response()
}

fn bad_request(msg: &str) -> Response {
    (
        StatusCode::BAD_REQUEST,
        Json(ErrorResponse {
            error: msg.to_string(),
        }),
    )
        .into_response()
}

pub async fn create_item(
    State(store): State<Store>,
    Json(req): Json<CreateItemRequest>,
) -> Response {
    if req.name.is_empty() {
        return bad_request("name is required");
    }
    let item = store.create(req);
    let location = format!("/items/{}", item.id);
    let mut resp = (StatusCode::CREATED, Json(item)).into_response();
    resp.headers_mut().insert(
        axum::http::header::LOCATION,
        HeaderValue::from_str(&location).expect("valid header"),
    );
    resp
}

#[derive(Debug, Deserialize)]
pub struct ListParams {
    pub limit: Option<usize>,
    pub offset: Option<usize>,
}

pub async fn list_items(
    State(store): State<Store>,
    Query(params): Query<ListParams>,
) -> impl IntoResponse {
    let limit = params.limit.filter(|&l| l > 0).unwrap_or(20);
    let offset = params.offset.unwrap_or(0);
    let (items, total) = store.list(limit, offset);
    Json(ListResponse {
        items,
        total,
        limit,
        offset,
    })
}

pub async fn get_item(State(store): State<Store>, Path(id): Path<String>) -> Response {
    match store.get(&id) {
        Some(item) => Json(item).into_response(),
        None => not_found(),
    }
}

pub async fn update_item(
    State(store): State<Store>,
    Path(id): Path<String>,
    Json(req): Json<UpdateItemRequest>,
) -> Response {
    if req.name.is_empty() {
        return bad_request("name is required");
    }
    match store.update(&id, req) {
        Some(item) => Json(item).into_response(),
        None => not_found(),
    }
}

pub async fn delete_item(State(store): State<Store>, Path(id): Path<String>) -> Response {
    if store.delete(&id) {
        StatusCode::NO_CONTENT.into_response()
    } else {
        not_found()
    }
}
