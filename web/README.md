# PlatePilot Web (placeholder)

Reserved for the frontend project. Nothing is implemented yet.

Planned scope (M6-06): chat view, restaurant candidate cards, citations with
source and snapshot time, and a run trace panel.

The frontend will talk to `chat-service` over its HTTP/SSE API:

- `POST /v1/threads/{thread_id}/messages` (SSE)
- `GET /v1/restaurants/search`
- `GET /v1/restaurants/{restaurant_id}/evidence`

Keep this directory free of Go code so the frontend toolchain (Node, Vite, …) can
own it without interfering with the Go module at the repository root.
