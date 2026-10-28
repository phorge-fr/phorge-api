---
type: reference
audience: developer or operator who works with roles and API keys
verified: 2026-10-28
---

# Memberships and API keys

- **A membership gives a user a role** on a tenant (`admin`, `operator`, `user`, `viewer`). Memberships are objects of the tenant's namespace, read and written only by the admins of the tenant (the relations `can_read_memberships` and `can_write_memberships`) or a platform administrator. A membership cannot be edited, because a role is an OpenFGA tuple that cannot be changed in place: to change a role, delete the membership, and create another. There is one per user and tenant: its name is derived from the email, so a second one is a `409`. The owner of a tenant already is admin through the tenant and a membership for it is refused (`409`, `owner_is_admin`): to hand a tenant over, give someone else the `admin` role. Only users are supported, a group is refused (nothing writes the tuples a group needs).
- **Deleting a membership is not an instant revocation**: the object goes at once, its tuples within about a minute (measured on the cluster in the test below).
- **An API key is personal**: it belongs to the user who created it, not to a tenant, and acts as them within its scopes (`tenants:read`, `tenants:write`, `instances:read|write`, `images:read|write`, `memberships:read|write`). It is `phk_<id>_<secret>`: the id locates the object with one get, the secret is 32 random bytes in hex. The answer to its creation holds the key, **once**; only the SHA-256 of the secret is stored (`spec.hash`), so no read ever shows it again.
- **Only a person manages keys**, signed in with Authentik: a key cannot create, read, or delete keys. `API_KEY_CREATION` says who creates and changes them (the platform administrators, or any person); anyone lists and revokes their own (a key can always be revoked). Someone else's key answers `404`.
- **A key never exceeds its owner**, because the owner is what is asked at every request. A key expires: with `API_KEY_MAX_LIFETIME` set, `expiresAt` is required and capped to it.
- **A key stops at its `expiresAt` to the second**: the authentication checks the date itself and does not wait for the composition. A key that is being deleted stops at once too. The list shows `expired` as soon as the date has passed.
- A key cannot be rotated: delete it and create another.
