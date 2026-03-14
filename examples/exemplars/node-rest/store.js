// In-memory item store with insertion-order iteration.

export class NotFoundError extends Error {
  constructor(id) {
    super(`item not found: ${id}`);
    this.name = 'NotFoundError';
  }
}

export class Store {
  #items = new Map();
  #order = [];

  create(name, description) {
    const id = crypto.randomUUID();
    const now = new Date().toISOString();
    const item = {
      id,
      name,
      description,
      created_at: now,
      updated_at: now,
    };
    this.#items.set(id, item);
    this.#order.push(id);
    return { ...item };
  }

  get(id) {
    const item = this.#items.get(id);
    if (!item) throw new NotFoundError(id);
    return { ...item };
  }

  list(limit = 20, offset = 0) {
    const total = this.#order.length;
    const slice = this.#order.slice(offset, offset + limit);
    const items = slice.map(id => ({ ...this.#items.get(id) }));
    return { items, total };
  }

  update(id, name, description) {
    const item = this.#items.get(id);
    if (!item) throw new NotFoundError(id);
    item.name = name;
    item.description = description;
    item.updated_at = new Date().toISOString();
    return { ...item };
  }

  delete(id) {
    if (!this.#items.has(id)) throw new NotFoundError(id);
    this.#items.delete(id);
    const idx = this.#order.indexOf(id);
    if (idx !== -1) this.#order.splice(idx, 1);
  }
}
