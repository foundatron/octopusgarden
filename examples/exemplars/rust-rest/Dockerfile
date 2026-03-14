FROM rust:1.84-alpine AS builder
# hadolint ignore=DL3018
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY . .
RUN cargo build --release

FROM alpine:3
COPY --from=builder /app/target/release/rust-rest /rust-rest
EXPOSE 8080
CMD ["/rust-rest"]
