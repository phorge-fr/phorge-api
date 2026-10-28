---
type: explanation
audience: developer who needs to understand how the API decides what a caller may do
verified: 2026-10-28
---

# How the API authorizes a request

Authentication tells who calls, OpenFGA decides what they may do (`internal/authz`). Every decision is a `Check` in the `phorge-api` store on the object `tenant:<name>`, for the subject of the identity (`user:<email>`, which is also the owner of a key):

| Action | Relation | Scope of a key |
|---|---|---|
| read the tenant | `can_read_tenant` | `tenants:read` |
| read instances, images | `can_read_instances`, `can_read_images` | `instances:read`, `images:read` |
| write instances | `can_write_instances` | `instances:write` |
| write images | `can_write_images` | `images:write` |
| read, write memberships and keys | `can_read_memberships`, `can_write_memberships` | `memberships:read`, `memberships:write` |

A user gets those through a role (`admin` over `operator` over `user` over `viewer`). A key does what its owner may **and** what its scopes name: the scope is checked first (`instances:write` does not give `instances:read`), then OpenFGA is asked about the owner. `Authorizer.Require(action, ...)` is the middleware of a route: `404` when the caller cannot read the tenant (a tenant that is not yours is not shown to exist), `403` when it can but may not do this, `503` when OpenFGA cannot answer. A decision that cannot be taken is never an "allowed". `Authorizer.PlatformAdmin` tells who creates, resizes and deletes tenants: the members of the Authentik group `PLATFORM_ADMIN_GROUP` (the model has no relation for it). A token carries its groups; a key does not, so the API asks Authentik whether its owner is a member of that group (`internal/authentik`, `AUTHENTIK_URL` and `AUTHENTIK_TOKEN`: the token of a service account that may read that one group and nothing else, the members remembered for `AUTHENTIK_CACHE_TTL`; if the group cannot be read, that is an error, never "nobody is in it"): a key stops being an administrator's within `AUTHENTIK_CACHE_TTL` of its owner leaving the group. The scopes `tenants:read` and `tenants:write` gate what a platform administrator's key does on tenants. `GET /v1/whoami` says whether the caller may write tenants. Members are added in Authentik, the group itself is created by the blueprint in Hangar.

OpenFGA is reached with `OPENFGA_URL`, `OPENFGA_API_KEY` and `OPENFGA_STORE_ID`, and `CA_FILE` for the internal CA of `openfga.core.phorge` (the verification is never turned off).

To run the tests against the real model, see [Run the integration tests](../how-to/run-the-integration-tests.md).
