---
type: reference
audience: developer or operator who needs to know which credentials the API accepts
verified: 2026-11-01
---

# Authentication

`Authorization: Bearer <credential>`, the credential is either:

- an access token of Authentik (signature against the JWKS of `OIDC_ISSUER`, issuer, audience, expiry, and a verified `email` claim, which is the user);
- an API key: the access token that Authentik gives the service account of the key (`client_credentials`), with no `email` claim and a `preferred_username` that starts with `apikey-` and is a key name (lowercase letters, digits and hyphens, 63 characters at most). The caller is of kind `servicekey` and its subject is `apikey:<name>`. It has no owner and no group: it can do what OpenFGA holds for that subject and nothing else, and it is never a platform administrator. The tuples of a key carry an expiry as an OpenFGA condition: the API sends the current time (UTC) with every `Check` and `ListObjects`, and a key past its date is refused by OpenFGA itself. Only a person can create or revoke a key. A token that has an `email` is always a person, whatever its name.

Whatever is wrong with a credential, the answer is the same `401`; the reason is only logged. When Authentik cannot be reached the answer is `503`, not `401`.

To get a token from a terminal, see [Get a token with the device flow](../how-to/get-a-token-with-the-device-flow.md).
