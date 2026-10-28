---
type: reference
audience: developer or operator who needs to know which credentials the API accepts
verified: 2026-10-28
---

# Authentication

`Authorization: Bearer <credential>`, the credential is either:

- an access token of Authentik (signature against the JWKS of `OIDC_ISSUER`, issuer, audience, expiry, and a verified `email` claim, which is the user);
- a personal API key `phk_<id>_<secret>`, looked up as the `ApiKey` object `<id>` of the namespace `phorge-keys`. `spec.hash` is the SHA-256 in hex of the secret part as written. A key past its `expiresAt`, marked `status.expired`, being deleted or without an owner is refused. A key is **its owner**: the identity is `user:<owner>`, limited to the scopes of the key.

Whatever is wrong with a credential, the answer is the same `401`; the reason is only logged. When Authentik or the Kubernetes API cannot be reached the answer is `503`, not `401`.

To get a token from a terminal, see [Get a token with the device flow](../how-to/get-a-token-with-the-device-flow.md).
