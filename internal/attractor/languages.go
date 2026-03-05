package attractor

import "slices"

// LanguageTemplate holds language-specific code generation examples and configuration.
type LanguageTemplate struct {
	Name        string       // human-readable name: "Go", "Python", "Node.js", "Rust"
	BaseImage   string       // Docker base image: "golang:1.24-alpine"
	HTTPExample ExampleBlock // example entry file + Dockerfile for HTTP apps
	CLIExample  ExampleBlock // example entry file + Dockerfile for CLI apps
	GRPCSetup   string       // protoc/plugin installation steps for Dockerfile
	DepRules    string       // language-specific dependency rules
}

// ExampleBlock holds a language-specific example showing the file format convention.
type ExampleBlock struct {
	EntryFile    string // e.g. "main.go"
	EntryContent string // example source code
	Dockerfile   string // example Dockerfile content
}

// languageRegistry maps canonical language keys to their templates.
var languageRegistry = map[string]LanguageTemplate{
	"go": {
		Name:      "Go",
		BaseImage: "golang:1.24-alpine",
		HTTPExample: ExampleBlock{
			EntryFile: "main.go",
			EntryContent: `package main

import "net/http"

func main() {
	http.ListenAndServe(":8080", nil)
}`,
			Dockerfile: `FROM golang:1.24-alpine
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go mod tidy
RUN go build -o server .
CMD ["./server"]`,
		},
		CLIExample: ExampleBlock{
			EntryFile: "main.go",
			EntryContent: `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: myapp <command>")
		os.Exit(1)
	}
	fmt.Println("Hello from", os.Args[1])
}`,
			Dockerfile: `FROM golang:1.24-alpine
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go mod tidy
RUN go build -o /usr/local/bin/myapp .`,
		},
		GRPCSetup: "RUN apk add --no-cache protobuf-dev && go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest",
		DepRules:  "- For Go: use net/http (not gorilla/mux), use crypto/rand or math/rand for UUIDs (not google/uuid)\n- For Go: generate only go.mod with no \"require\" block (or minimal requires). In the Dockerfile, COPY all source files first, THEN run \"go mod tidy\" to resolve dependencies, THEN build. Example Dockerfile order: COPY go.mod ./ then COPY . . then RUN go mod tidy then RUN go build",
	},
	"python": {
		Name:      "Python",
		BaseImage: "python:3.12-slim",
		HTTPExample: ExampleBlock{
			EntryFile: "app.py",
			EntryContent: `from http.server import HTTPServer, BaseHTTPRequestHandler

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

HTTPServer(("", 8080), Handler).serve_forever()`,
			Dockerfile: `FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
CMD ["python", "app.py"]`,
		},
		CLIExample: ExampleBlock{
			EntryFile: "app.py",
			EntryContent: `import argparse

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command")
    args = parser.parse_args()
    print(f"Hello from {args.command}")

if __name__ == "__main__":
    main()`,
			Dockerfile: `FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
RUN ln -s /app/app.py /usr/local/bin/myapp && chmod +x /app/app.py`,
		},
		GRPCSetup: "RUN pip install grpcio grpcio-tools",
		DepRules:  "- For Python: prefer standard library (http.server, argparse, json) over third-party packages\n- For Python: generate only requirements.txt; use \"pip install\" in the Dockerfile",
	},
	"node": {
		Name:      "Node.js",
		BaseImage: "node:22-alpine",
		HTTPExample: ExampleBlock{
			EntryFile: "index.js",
			EntryContent: `const http = require("http");

const server = http.createServer((req, res) => {
  res.writeHead(200);
  res.end("ok");
});

server.listen(8080);`,
			Dockerfile: `FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install
COPY . .
CMD ["node", "index.js"]`,
		},
		CLIExample: ExampleBlock{
			EntryFile: "index.js",
			EntryContent: `const args = process.argv.slice(2);
if (args.length < 1) {
  console.error("usage: myapp <command>");
  process.exit(1);
}
console.log("Hello from", args[0]);`,
			Dockerfile: `FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install
COPY . .
RUN ln -s /app/index.js /usr/local/bin/myapp && chmod +x /app/index.js`,
		},
		GRPCSetup: "RUN npm install @grpc/grpc-js @grpc/proto-loader",
		DepRules:  "- For Node.js: prefer built-in modules (http, fs, path, crypto) over npm packages\n- For Node.js: generate only package.json; use \"npm install\" in the Dockerfile",
	},
	"rust": {
		Name:      "Rust",
		BaseImage: "rust:1.84-alpine",
		HTTPExample: ExampleBlock{
			EntryFile: "src/main.rs",
			EntryContent: `use axum::{routing::get, Router};

#[tokio::main]
async fn main() {
    let app = Router::new().route("/", get(|| async { "ok" }));
    let listener = tokio::net::TcpListener::bind("0.0.0.0:8080").await.unwrap();
    axum::serve(listener, app).await.unwrap();
}`,
			Dockerfile: `FROM rust:1.84-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release

FROM alpine:3.21
COPY --from=builder /app/target/release/myapp /usr/local/bin/
CMD ["myapp"]`,
		},
		CLIExample: ExampleBlock{
			EntryFile: "src/main.rs",
			EntryContent: `use std::env;

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() < 2 {
        eprintln!("usage: myapp <command>");
        std::process::exit(1);
    }
    println!("Hello from {}", args[1]);
}`,
			Dockerfile: `FROM rust:1.84-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release

FROM alpine:3.21
COPY --from=builder /app/target/release/myapp /usr/local/bin/myapp`,
		},
		GRPCSetup: "RUN apk add --no-cache protobuf-dev\n# Add tonic-build to build.rs for protobuf compilation",
		DepRules:  "- For Rust: prefer standard library where possible\n- For Rust: generate Cargo.toml with dependencies; use multi-stage Dockerfile (builder + runtime)\n- For Rust: do NOT generate Cargo.lock — let cargo resolve dependencies at build time",
	},
}

// LookupLanguage returns the template for the given language key, or false if not found.
func LookupLanguage(lang string) (LanguageTemplate, bool) {
	t, ok := languageRegistry[lang]
	return t, ok
}

// SupportedLanguages returns the sorted list of registered language keys.
func SupportedLanguages() []string {
	keys := make([]string, 0, len(languageRegistry))
	for k := range languageRegistry {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
