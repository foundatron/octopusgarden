"""Smoke tests for the in-memory Store: create/get/list/update/delete round-trip."""

import unittest

from store import NotFoundError, Store


class TestStore(unittest.TestCase):
    def setUp(self):
        self.store = Store()

    def test_create_and_get(self):
        item = self.store.create("widget", "a small widget")
        self.assertEqual(item.name, "widget")
        self.assertEqual(item.description, "a small widget")
        self.assertTrue(item.id)
        self.assertTrue(item.created_at.endswith("Z"))

        fetched = self.store.get(item.id)
        self.assertEqual(fetched.id, item.id)
        self.assertEqual(fetched.name, item.name)

    def test_get_returns_copy(self):
        item = self.store.create("original", "")
        copy = self.store.get(item.id)
        copy.name = "mutated"
        # Mutation of the returned copy must not affect the stored item.
        self.assertEqual(self.store.get(item.id).name, "original")

    def test_get_not_found(self):
        with self.assertRaises(NotFoundError):
            self.store.get("does-not-exist")

    def test_list_empty(self):
        items, total = self.store.list()
        self.assertEqual(items, [])
        self.assertEqual(total, 0)

    def test_list_pagination(self):
        for i in range(5):
            self.store.create(f"item-{i}", "")
        items, total = self.store.list(limit=2, offset=0)
        self.assertEqual(total, 5)
        self.assertEqual(len(items), 2)
        self.assertEqual(items[0].name, "item-0")
        self.assertEqual(items[1].name, "item-1")

        page2, _ = self.store.list(limit=2, offset=2)
        self.assertEqual(len(page2), 2)
        self.assertEqual(page2[0].name, "item-2")

    def test_update(self):
        item = self.store.create("old", "old desc")
        updated = self.store.update(item.id, "new", "new desc")
        self.assertEqual(updated.name, "new")
        self.assertEqual(updated.description, "new desc")
        self.assertEqual(self.store.get(item.id).name, "new")

    def test_update_not_found(self):
        with self.assertRaises(NotFoundError):
            self.store.update("no-such-id", "x", "")

    def test_delete(self):
        item = self.store.create("to-delete", "")
        self.store.delete(item.id)
        with self.assertRaises(NotFoundError):
            self.store.get(item.id)
        _, total = self.store.list()
        self.assertEqual(total, 0)

    def test_delete_not_found(self):
        with self.assertRaises(NotFoundError):
            self.store.delete("no-such-id")

    def test_insertion_order_preserved(self):
        names = ["alpha", "beta", "gamma"]
        for n in names:
            self.store.create(n, "")
        items, _ = self.store.list()
        self.assertEqual([i.name for i in items], names)


if __name__ == "__main__":
    unittest.main()
