// Node.js REST exemplar: items CRUD API using stdlib only.

import { createServer } from 'node:http';

import { Store } from './store.js';
import { route } from './router.js';

const PORT = 8080;

async function main() {
  const store = new Store();

  const server = createServer((req, res) => {
    Promise.resolve(route(req, res, store)).catch(err => {
      console.error('unhandled error', err);
      if (!res.headersSent) {
        const body = '{"error":"internal server error"}';
        res.writeHead(500, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) });
        res.end(body);
      }
    });
  });

  const shutdown = () => server.close(() => process.exit(0));
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  await new Promise((resolve, reject) => {
    server.listen(PORT, '0.0.0.0', resolve);
    server.once('error', reject);
  });

  console.log(`server listening on :${PORT}`);
}

main().catch(err => {
  console.error('startup error:', err);
  process.exit(1);
});
