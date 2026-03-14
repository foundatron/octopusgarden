// URL dispatch by method and path.

import {
  handleHealth,
  handleCreateItem,
  handleListItems,
  handleGetItem,
  handleUpdateItem,
  handleDeleteItem,
} from './handlers.js';

const ITEM_ID_RE = /^\/items\/([^/]+)$/;

function notFound(res) {
  const body = '{"error":"not found"}';
  res.writeHead(404, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) });
  res.end(body);
}

function methodNotAllowed(res) {
  const body = '{"error":"method not allowed"}';
  res.writeHead(405, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) });
  res.end(body);
}

export function route(req, res, store) {
  const url = new URL(req.url, 'http://localhost');
  const path = url.pathname;
  const method = req.method;

  if (path === '/health' && method === 'GET') return handleHealth(req, res, store);
  if (path === '/items' && method === 'GET') return handleListItems(req, res, store, url);
  if (path === '/items' && method === 'POST') return handleCreateItem(req, res, store);

  const m = ITEM_ID_RE.exec(path);
  if (m) {
    const id = decodeURIComponent(m[1]);
    if (method === 'GET') return handleGetItem(req, res, store, id);
    if (method === 'PUT') return handleUpdateItem(req, res, store, id);
    if (method === 'DELETE') return handleDeleteItem(req, res, store, id);
    return methodNotAllowed(res);
  }

  return notFound(res);
}
