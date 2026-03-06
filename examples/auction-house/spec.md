# Auction House

A real-time auction platform where users place bids via WebSocket and the current highest bid is
broadcast to all connected clients.

## Overview

The auction house service manages items up for auction. Clients connect via WebSocket to receive
live bid updates. Bids can also be placed via HTTP. The service broadcasts the new highest bid to
all WebSocket subscribers whenever a bid is accepted.

## Endpoints

### HTTP

- `POST /auctions` — Create a new auction item. Request body:
  `{"item": "string", "starting_price": number}` Response:
  `{"id": "string", "item": "string", "current_price": number}`

- `GET /auctions/{id}` — Get the current state of an auction. Response:
  `{"id": "string", "item": "string", "current_price": number, "bids": number}`

- `POST /auctions/{id}/bid` — Place a bid on an auction item. Request body: `{"amount": number}`
  Response 200: `{"accepted": true, "amount": number}` if the bid is higher than the current price.
  Response 400: `{"accepted": false, "reason": "string"}` if the bid is too low.

### WebSocket

- `GET /ws/auctions/{id}` — Subscribe to live bid updates for an auction. The server sends a JSON
  message whenever a new highest bid is accepted:
  `{"event": "bid", "amount": number, "bids": number}`

## Behavior

- When a bid is accepted via HTTP, the service immediately broadcasts a `bid` event to all WebSocket
  subscribers for that auction.
- Bids must be strictly greater than the current price to be accepted.
- The service must handle multiple concurrent WebSocket connections per auction.
