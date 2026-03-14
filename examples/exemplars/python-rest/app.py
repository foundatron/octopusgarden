"""Python REST exemplar: items CRUD API using stdlib only."""

import re
import signal
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn

from handlers import (
    handle_create_item,
    handle_delete_item,
    handle_get_item,
    handle_health,
    handle_list_items,
    handle_update_item,
    write_error,
)
from store import Store

_ITEM_ID_RE = re.compile(r"^/items/([^/]+)$")


class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True


class ItemHandler(BaseHTTPRequestHandler):
    store: Store

    def log_message(self, fmt, *args):  # noqa: D102
        pass  # suppress default access log; structured logging preferred

    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/health":
            handle_health(self, self.store)
        elif path == "/items":
            handle_list_items(self, self.store)
        else:
            m = _ITEM_ID_RE.match(path)
            if m:
                handle_get_item(self, self.store, m.group(1))
            else:
                self._not_found()

    def do_POST(self):
        if self.path.split("?")[0] == "/items":
            handle_create_item(self, self.store)
        else:
            self._not_found()

    def do_PUT(self):
        m = _ITEM_ID_RE.match(self.path.split("?")[0])
        if m:
            handle_update_item(self, self.store, m.group(1))
        else:
            self._not_found()

    def do_DELETE(self):
        m = _ITEM_ID_RE.match(self.path.split("?")[0])
        if m:
            handle_delete_item(self, self.store, m.group(1))
        else:
            self._not_found()

    def _not_found(self):
        write_error(self, 404, "not found")


def main():
    store = Store()
    server = ThreadingHTTPServer(("0.0.0.0", 8080), ItemHandler)
    # Inject store via class attribute — stdlib pattern for sharing state with handlers.
    server.RequestHandlerClass.store = store

    def shutdown(signum, frame):
        server.shutdown()
        sys.exit(0)

    signal.signal(signal.SIGINT, shutdown)
    signal.signal(signal.SIGTERM, shutdown)

    print("server listening on :8080", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
