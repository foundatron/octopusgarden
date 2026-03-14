// HTTP handler functions for the items REST API.

import { NotFoundError } from './store.js';

const MAX_BODY = 1 << 20; // 1 MiB

function writeJSON(res, status, data, extraHeaders = {}) {
  const body = JSON.stringify(data);
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
    ...extraHeaders,
  });
  res.end(body);
}

function writeError(res, status, message) {
  writeJSON(res, status, { error: message });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    let done = false;
    req.on('data', chunk => {
      if (done) return;
      size += chunk.length;
      if (size > MAX_BODY) {
        done = true;
        reject(new Error('body too large'));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on('end', () => { if (!done) resolve(Buffer.concat(chunks).toString()); });
    req.on('error', err => { if (!done) reject(err); });
  });
}

export function handleHealth(_req, res, _store) {
  writeJSON(res, 200, { status: 'ok' });
}

async function parseItemBody(req, res) {
  let raw;
  try {
    raw = await readBody(req);
  } catch {
    writeError(res, 400, 'request body too large');
    return null;
  }
  let body;
  try {
    body = JSON.parse(raw);
  } catch {
    writeError(res, 400, 'invalid JSON');
    return null;
  }
  const name = (body.name ?? '').trim();
  if (!name) {
    writeError(res, 400, 'name is required');
    return null;
  }
  return { name, description: body.description ?? '' };
}

export async function handleCreateItem(req, res, store) {
  const parsed = await parseItemBody(req, res);
  if (!parsed) return;
  const item = store.create(parsed.name, parsed.description);
  writeJSON(res, 201, item, { 'Location': `/items/${item.id}` });
}

export function handleListItems(_req, res, store, url) {
  let limit = parseInt(url.searchParams.get('limit') ?? '20', 10);
  let offset = parseInt(url.searchParams.get('offset') ?? '0', 10);
  if (!Number.isFinite(limit) || limit <= 0) limit = 20;
  if (!Number.isFinite(offset) || offset < 0) offset = 0;
  const { items, total } = store.list(limit, offset);
  writeJSON(res, 200, { items, total, limit, offset });
}

export function handleGetItem(_req, res, store, id) {
  try {
    writeJSON(res, 200, store.get(id));
  } catch (err) {
    if (err instanceof NotFoundError) { writeError(res, 404, 'item not found'); return; }
    throw err;
  }
}

export async function handleUpdateItem(req, res, store, id) {
  const parsed = await parseItemBody(req, res);
  if (!parsed) return;
  try {
    writeJSON(res, 200, store.update(id, parsed.name, parsed.description));
  } catch (err) {
    if (err instanceof NotFoundError) { writeError(res, 404, 'item not found'); return; }
    throw err;
  }
}

export function handleDeleteItem(_req, res, store, id) {
  try {
    store.delete(id);
    res.writeHead(204, { 'Content-Length': '0' });
    res.end();
  } catch (err) {
    if (err instanceof NotFoundError) { writeError(res, 404, 'item not found'); return; }
    throw err;
  }
}
