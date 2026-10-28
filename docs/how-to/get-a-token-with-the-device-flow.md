---
type: how-to
audience: developer who needs an Authentik access token from a terminal
verified: 2026-10-28
---

# Get a token with the device flow

Use this to call the API from a terminal with your own Authentik identity.

## Steps

```bash
# a token from a terminal, with the device code flow of Authentik
curl -s https://auth.phorge.fr/application/o/device/ -d client_id=<OIDC_AUDIENCE> -d 'scope=openid email profile offline_access'
# approve verification_uri_complete in a browser, then poll /application/o/token/ with
# grant_type=urn:ietf:params:oauth:grant-type:device_code and --data-urlencode "device_code=..."
# (the code holds characters that `curl -d` does not encode, Authentik then answers invalid_grant for ever;
# while you have not approved it answers authorization_pending)
```

## Check that it worked

Call `GET /v1/whoami` with `Authorization: Bearer <access_token>`; it names you. Authentik answers `authorization_pending` until you approve in the browser.

## Related

- [Authentication](../reference/authentication.md)
