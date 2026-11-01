---
type: reference
audience: developer or operator who needs to know which credentials the API accepts
verified: 2026-11-01
---

# Authentication

`Authorization: Bearer <credential>`, the credential is either:

- an access token of Authentik (signature against the JWKS of `OIDC_ISSUER`, issuer, audience, expiry, and a verified `email` claim, which is the user);
- a personal API key `phk_<id>_<secret>`, looked up as the `ApiKey` object `<id>` of the namespace `phorge-keys`. `spec.hash` is the SHA-256 in hex of the secret part as written. A key past its `expiresAt`, marked `status.expired`, being deleted or without an owner is refused. A key is **its owner**: the identity is `user:<owner>`, limited to the scopes of the key.
- a service key, when `OIDC_KEY_PREFIX` is set: an access token of Authentik with no `email` claim whose `preferred_username` starts with that prefix and is a key name (lowercase letters, digits and hyphens, 63 characters at most). It is the token of a service account that holds an API key. The caller is of kind `servicekey` and its subject is `apikey:<name>`. It has no owner, no scope list and no group: it can do what OpenFGA holds for that subject and nothing else, and it is never a platform administrator. Only a person can create or revoke a key. With the variable empty, a token with no `email` is refused. A token that has an `email` is always a person, whatever its name.

Whatever is wrong with a credential, the answer is the same `401`; the reason is only logged. When Authentik or the Kubernetes API cannot be reached the answer is `503`, not `401`.

To get a token from a terminal, see [Get a token with the device flow](../how-to/get-a-token-with-the-device-flow.md).
