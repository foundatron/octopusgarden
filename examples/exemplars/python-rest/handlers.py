"""HTTP handler functions for the items REST API."""

import dataclasses
import json
from urllib.parse import parse_qs, urlparse

from store import NotFoundError, Store


def write_json(handler, status: int, data) -> None:
    body = json.dumps(data).encode()
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def write_error(handler, status: int, msg: str) -> None:
    write_json(handler, status, {"error": msg})


def handle_health(handler, store: Store) -> None:
    write_json(handler, 200, {"status": "ok"})


def handle_create_item(handler, store: Store) -> None:
    try:
        length = int(handler.headers.get("Content-Length", 0))
    except ValueError:
        length = 0
    if length > 1 << 20:
        write_error(handler, 400, "request body too large")
        return
    try:
        body = json.loads(handler.rfile.read(length))
    except (json.JSONDecodeError, ValueError):
        write_error(handler, 400, "invalid JSON")
        return
    name = body.get("name", "").strip()
    if not name:
        write_error(handler, 400, "name is required")
        return
    description = body.get("description", "")
    item = store.create(name, description)
    handler.send_response(201)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Location", f"/items/{item.id}")
    body_bytes = json.dumps(dataclasses.asdict(item)).encode()
    handler.send_header("Content-Length", str(len(body_bytes)))
    handler.end_headers()
    handler.wfile.write(body_bytes)


def handle_list_items(handler, store: Store) -> None:
    parsed = urlparse(handler.path)
    params = parse_qs(parsed.query)
    limit = 20
    offset = 0
    try:
        if "limit" in params:
            v = int(params["limit"][0])
            if v > 0:
                limit = v
    except ValueError:
        pass
    try:
        if "offset" in params:
            v = int(params["offset"][0])
            if v >= 0:
                offset = v
    except ValueError:
        pass
    items, total = store.list(limit, offset)
    write_json(
        handler,
        200,
        {
            "items": [dataclasses.asdict(i) for i in items],
            "total": total,
            "limit": limit,
            "offset": offset,
        },
    )


def handle_get_item(handler, store: Store, item_id: str) -> None:
    try:
        item = store.get(item_id)
    except NotFoundError:
        write_error(handler, 404, "item not found")
        return
    write_json(handler, 200, dataclasses.asdict(item))


def handle_update_item(handler, store: Store, item_id: str) -> None:
    try:
        length = int(handler.headers.get("Content-Length", 0))
    except ValueError:
        length = 0
    if length > 1 << 20:
        write_error(handler, 400, "request body too large")
        return
    try:
        body = json.loads(handler.rfile.read(length))
    except (json.JSONDecodeError, ValueError):
        write_error(handler, 400, "invalid JSON")
        return
    name = body.get("name", "").strip()
    if not name:
        write_error(handler, 400, "name is required")
        return
    description = body.get("description", "")
    try:
        item = store.update(item_id, name, description)
    except NotFoundError:
        write_error(handler, 404, "item not found")
        return
    write_json(handler, 200, dataclasses.asdict(item))


def handle_delete_item(handler, store: Store, item_id: str) -> None:
    try:
        store.delete(item_id)
    except NotFoundError:
        write_error(handler, 404, "item not found")
        return
    handler.send_response(204)
    handler.send_header("Content-Length", "0")
    handler.end_headers()
