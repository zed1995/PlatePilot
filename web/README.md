# PlatePilot Admin Console

Read-only operator console for PlatePilot. It inspects the stored data through
the `chat-service` admin API; it never writes.

Stack: Vite + React 18 + TypeScript + Tailwind CSS + Radix UI primitives +
hand-copied shadcn-style components + Lucide icons + Motion + TanStack Query +
React Router.

## Prerequisites

`chat-service` must be running with the admin surface enabled. It is disabled
by default:

```sh
ADMIN_ENABLED=true
```

The admin API is additionally restricted to loopback clients, so run the
service bound to a local address (e.g. `HTTP_ADDR=127.0.0.1:8080`).

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

## Admin endpoints

All routes are mounted under `/admin/v1` and are loopback-only:

- `GET /overview`
- `GET /boundaries`
- `GET /restaurants`, `GET /restaurants/{id}`
- `GET /restaurants/{id}/reviews`, `.../summaries`, `.../documents`
- `GET /documents`, `GET /documents/{id}`
- `GET /batches`, `GET /batches/{id}`
- `POST /debug/search`, `POST /debug/evidence` (retrieval probes; read-only)

Keep this directory free of Go code so the Node toolchain owns it without
interfering with the Go module at the repository root.
