---
type: reference
audience: developer calling the API
verified: 2026-11-08
---

# Endpoints

| Route | |
|---|---|
| `GET /docs` | Swagger UI |
| `GET /openapi.yaml` | The OpenAPI document, written by hand in `internal/openapi/openapi.yaml`. A test compares it with the registered routes, so a route and its description go in together |
| `GET /v1/whoami` | Who the credential stands for |
| `GET /v1/limits` | What the API enforces on a request: the bounds of an instance and the value each takes when a request leaves it out, the longest names. Any credential; nothing about a tenant |
| `GET /v1/tenants`, `GET /v1/tenants/{t}` | The tenants you can see (the platform administrators see all of them) |
| `POST /v1/tenants`, `PATCH /v1/tenants/{t}`, `DELETE /v1/tenants/{t}` | Create, resize and delete a tenant: platform administrators only |
| `GET`, `POST /v1/tenants/{t}/images`, `GET`, `DELETE /v1/tenants/{t}/images/{name}` | The images of a tenant: read with a role or a key tuple (`images:read`), write with `operator` or `images:write` |
| `GET /v1/tenants/{t}/quota` | What the plan of the tenant allows (instances, vCPU, memory, disk, networks) and what it holds. Read with the right of the instances |
| `GET`, `POST /v1/tenants/{t}/profiles`, `GET`, `DELETE /v1/tenants/{t}/profiles/{name}` | The sizes an instance can take: the profiles `s`, `m` and `l` every tenant is created with, and the ones its users make. Read with any role or `profiles:read`, write with `operator` or `profiles:write` |
| `GET`, `POST /v1/tenants/{t}/networks`, `GET`, `DELETE /v1/tenants/{t}/networks/{name}` | The networks of a tenant, which comes without one: read with a role (`viewer` and above) or `networks:read`, write with `operator` or `networks:write` |
| `GET`, `POST /v1/tenants/{t}/memberships`, `GET`, `DELETE /v1/tenants/{t}/memberships/{name}` | The roles of the users: admins of the tenant, and the keys with `memberships:read` and `memberships:write` on it |
| `GET /v1/apikeys/scopes` | The scopes a key can be given, `<resource>:<verb>`, with what each does. For a person, like the other routes of the keys |
| `GET`, `POST /v1/apikeys`, `GET`, `PATCH`, `DELETE /v1/apikeys/{id}` | Your own API keys, with a token of Authentik (a key cannot manage keys). Creating: platform administrators for now. `PATCH` only renames |
| `GET`, `POST /v1/tenants/{t}/instances`, `GET`, `PATCH`, `DELETE /v1/tenants/{t}/instances/{name}` | The virtual machines: read with a role or a key tuple (`instances:read`), write with `user` or `instances:write` |
| `/healthz`, `/readyz` (503 while shutting down), `/metrics` | Probes and metrics |

Credentials are described in [Authentication](authentication.md).
