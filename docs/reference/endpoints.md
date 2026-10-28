---
type: reference
audience: developer calling the API
verified: 2026-10-28
---

# Endpoints

| Route | |
|---|---|
| `GET /docs` | Swagger UI |
| `GET /openapi.yaml` | The OpenAPI document, written by hand in `internal/openapi/openapi.yaml`. A test compares it with the registered routes, so a route and its description go in together |
| `GET /v1/whoami` | Who the credential stands for |
| `GET /v1/tenants`, `GET /v1/tenants/{t}` | The tenants you can see (the platform administrators see all of them) |
| `POST /v1/tenants`, `PATCH /v1/tenants/{t}`, `DELETE /v1/tenants/{t}` | Create, resize and delete a tenant: platform administrators only |
| `GET`, `POST /v1/tenants/{t}/images`, `GET`, `DELETE /v1/tenants/{t}/images/{name}` | The images of a tenant: read with `images:read` or a role, write with `operator` or `images:write` |
| `GET`, `POST /v1/tenants/{t}/memberships`, `GET`, `DELETE /v1/tenants/{t}/memberships/{name}` | The roles of the users: admins of the tenant (a key of one, with `memberships:read` and `memberships:write`) |
| `GET`, `POST /v1/apikeys`, `GET`, `PATCH`, `DELETE /v1/apikeys/{id}` | Your own API keys, with a token of Authentik (a key cannot manage keys). Creating and changing: platform administrators for now |
| `GET`, `POST /v1/tenants/{t}/instances`, `GET`, `PATCH`, `DELETE /v1/tenants/{t}/instances/{name}` | The virtual machines: read with `instances:read` or a role, write with `user` or `instances:write` |
| `/healthz`, `/readyz` (503 while shutting down), `/metrics` | Probes and metrics |

Credentials are described in [Authentication](authentication.md).
