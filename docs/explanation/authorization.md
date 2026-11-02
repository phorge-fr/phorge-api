---
type: explanation
audience: developer who needs to understand how the API decides what a caller may do
verified: 2026-10-28
---

# How the API authorizes a request

Authentication tells who calls, OpenFGA decides what they may do (`internal/authz`). Every decision is a `Check` in the `phorge-api` store on the object `tenant:<name>`, for the subject of the identity (`user:<email>` for a person, `apikey:<name>` for a key):

| Action | Relation | Scope of a key |
|---|---|---|
| read the tenant | `can_read_tenant` | `tenants:read` |
| read instances, images | `can_read_instances`, `can_read_images` | `instances:read`, `images:read` |
| write instances | `can_write_instances` | `instances:write` |
| write images | `can_write_images` | `images:write` |
| read, write memberships, and keys | `can_read_memberships`, `can_write_memberships` | `memberships:read`, `memberships:write` |

A user gets those through a role (`admin` over `operator` over `user` over `viewer`). A key gets them from tuples of its own, `key_<resource>_<action>` on the tenant, one per scope and tenant, each with the condition `not_expired`: the API sends the current time (UTC) with every `Check` and `ListObjects`, and past its date OpenFGA itself refuses the key, with nothing deleted. No role is derived for a key, and no table in the API widens what its tuples say (`instances:write` does not give `instances:read`). `Authorizer.Require(action, ...)` is the middleware of a route: `404` when the caller cannot read the tenant (a tenant that is not yours is not shown to exist), `403` when it can but may not do this, `503` when OpenFGA cannot answer. A decision that cannot be taken is never an "allowed". `Authorizer.PlatformAdmin` tells who creates, resizes and deletes tenants: the members of the Authentik group `PLATFORM_ADMIN_GROUP`, read from the `groups` claim of the token (the model has no relation for it). A key has no groups and is never a platform administrator. `GET /v1/whoami` says whether the caller may write tenants. Members are added in Authentik, the group itself is created by the blueprint in Hangar.

A key never gets more than its creator holds: when `POST /v1/apikeys` creates one, the API asks OpenFGA that the creator holds each scope on each tenant (a platform administrator holds everything on a tenant that exists). A scope that is refused, and a tenant that does not exist, give the same `403`.

OpenFGA is reached with `OPENFGA_URL`, `OPENFGA_API_KEY` and `OPENFGA_STORE_ID`, and `CA_FILE` for the internal CA of `openfga.core.phorge` (the verification is never turned off).

To run the tests against the real model, see [Run the integration tests](../how-to/run-the-integration-tests.md).
