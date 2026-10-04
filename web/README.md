# PlatePilot Web

Two surfaces in one app:

- **Operator console** (`/dashboard` … `/retrieval-debug`) — read-only. It
  inspects the stored data through the `chat-service` admin API; it never writes.
- **Agent Console** (`/agent`) — the conversational surface. It talks to `/v1`
  as a user, sends messages, and lets you watch a turn happen: streaming text,
  the live tool timeline, citations that open onto the document they quote,
  run replay, the clarification / confirmation gates, the memory list, and the
  `interpret` slot probe.

Stack: Vite + React 18 + TypeScript + Tailwind CSS + Radix UI primitives +
hand-copied shadcn-style components + Lucide icons + Motion + TanStack Query +
React Router.

## Prerequisites

`chat-service` must be running. The operator console additionally needs the admin
surface, which is disabled by default:

```sh
ADMIN_ENABLED=true
```

The admin API is additionally restricted to loopback clients, so run the
service bound to a local address (e.g. `HTTP_ADDR=127.0.0.1:8080`). The Agent
Console does not need `ADMIN_ENABLED`; it only uses `/v1`.

The database must be migrated before sending a message (`make migrate`); a
service on an old schema answers the first message with a 502.

`RESERVATION_ENABLED` is `false` by default, so the confirmation gate will not
appear unless it is switched on — the UI for it is complete either way.

## Develop

```sh
npm install
npm run dev
```

The Vite dev server proxies `/admin` and `/v1` to the backend. By default it
targets `http://127.0.0.1:8080`; override with `VITE_BACKEND_TARGET` when the
service runs elsewhere.

To call an absolute origin instead of the proxy, set `VITE_API_BASE`; that
origin must then be allowed by the service (`HTTP_CORS_ALLOW_ORIGINS`).

## Build and test

```sh
npm run build   # type-check + production build into dist/
npm test        # vitest
```

## Endpoints used

Operator console, mounted under `/admin/v1` and loopback-only:

- `GET /overview`
- `GET /boundaries`
- `GET /restaurants`, `GET /restaurants/{id}`
- `GET /restaurants/{id}/reviews`, `.../summaries`, `.../documents`
- `GET /documents`, `GET /documents/{id}`
- `GET /batches`, `GET /batches/{id}`
- `POST /debug/search`, `POST /debug/evidence` (retrieval probes; read-only)

Agent Console, under `/v1`, identified by the `X-User-ID` header:

- `POST|GET /conversations`, `GET /conversations/{id}`
- `GET|POST /conversations/{id}/messages` (POST is an SSE stream)
- `GET /conversations/{id}/candidates`, `GET /conversations/{id}/runs`
- `POST /conversations/{id}/confirm`
- `GET /runs/{id}`
- `GET|PATCH|DELETE /memories[/{id}]`
- `POST /restaurants/{search,evidence,interpret}`

Keep this directory free of Go code so the Node toolchain owns it without
interfering with the Go module at the repository root.

