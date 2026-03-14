"""In-memory item store with thread-safe access and insertion-order iteration."""

import dataclasses
import threading
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone


class NotFoundError(Exception):
    """Raised when an item is not found in the store."""


@dataclass
class Item:
    id: str
    name: str
    description: str
    created_at: str


class Store:
    def __init__(self):
        self._lock = threading.Lock()
        self._items: dict[str, Item] = {}
        self._order: list[str] = []

    def create(self, name: str, description: str) -> Item:
        item_id = str(uuid.uuid4())
        created_at = (
            datetime.now(timezone.utc)
            .isoformat(timespec="milliseconds")
            .replace("+00:00", "Z")
        )
        item = Item(
            id=item_id, name=name, description=description, created_at=created_at
        )
        with self._lock:
            self._items[item_id] = item
            self._order.append(item_id)
        return item

    def get(self, item_id: str) -> Item:
        with self._lock:
            item = self._items.get(item_id)
            if item is None:
                raise NotFoundError(item_id)
            return dataclasses.replace(item)

    def list(self, limit: int = 20, offset: int = 0) -> tuple[list[Item], int]:
        if limit <= 0:
            limit = 20
        if offset < 0:
            offset = 0
        with self._lock:
            total = len(self._order)
            if offset >= total:
                return [], total
            end = min(offset + limit, total)
            items = [self._items[i] for i in self._order[offset:end]]
        return items, total

    def update(self, item_id: str, name: str, description: str) -> Item:
        with self._lock:
            item = self._items.get(item_id)
            if item is None:
                raise NotFoundError(item_id)
            item.name = name
            item.description = description
            return dataclasses.replace(item)

    def delete(self, item_id: str) -> None:
        with self._lock:
            if item_id not in self._items:
                raise NotFoundError(item_id)
            del self._items[item_id]
            self._order.remove(item_id)
