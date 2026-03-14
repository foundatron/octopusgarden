mod handlers;
mod models;
mod store;

use axum::{
    routing::{delete, get, post, put},
    Router,
};

use crate::store::Store;

#[tokio::main]
async fn main() {
    let store = Store::new();

    let app = Router::new()
        .route("/health", get(handlers::health))
        .route("/items", post(handlers::create_item))
        .route("/items", get(handlers::list_items))
        .route("/items/{id}", get(handlers::get_item))
        .route("/items/{id}", put(handlers::update_item))
        .route("/items/{id}", delete(handlers::delete_item))
        .with_state(store);

    let listener = tokio::net::TcpListener::bind("0.0.0.0:8080").await.unwrap();
    println!("server listening on {}", listener.local_addr().unwrap());

    axum::serve(listener, app)
        .with_graceful_shutdown(async {
            tokio::signal::ctrl_c().await.unwrap();
        })
        .await
        .unwrap();
}
