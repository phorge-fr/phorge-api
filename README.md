# phorge-api

The Phorge API: authenticated users and API keys manage tenants, images, instances, memberships and API keys.
It creates `phorge.fr` objects on the `control` cluster (see `phorge-fr/Hangar`, `docs/phorge-api.md`), Crossplane turns them into Incus and OpenFGA objects.

Status: step 1 (authentication, OpenAPI document and Swagger UI) on top of step 0 (configuration, health, metrics, JSON logs, CI). The plan is `docs/phorge-api-implementation.md` in Hangar; `CLAUDE.md` tells an implementer how to start.

```bash
make vet lint test build
OIDC_ISSUER=... OIDC_AUDIENCE=... OPENFGA_URL=... OPENFGA_API_KEY=... OPENFGA_STORE_ID=... ./bin/phorge-api
```

Configuration is read from the environment and the process refuses to start, listing everything wrong, if a required value is missing:

| Variable | Required | Meaning |
|---|---|---|
| `OIDC_ISSUER` | yes | Authentik issuer, the JWKS is found from it |
| `OIDC_AUDIENCE` | yes | Client id of the Phorge application |
| `OPENFGA_URL` | yes | Base URL of OpenFGA |
| `OPENFGA_API_KEY` | yes | Bearer key for OpenFGA (never logged) |
| `OPENFGA_STORE_ID` | yes | Id of the `phorge-api` store |
| `CA_FILE` | no | PEM bundle added to the trusted roots (the internal CA) |
| `PLATFORM_ADMINS` | no | Comma-separated emails allowed to create, resize and delete tenants |
| `KUBE_CONTEXT` | no | Context of the kubeconfig, outside the cluster only (development). In a pod the service account is used |
| `LISTEN_ADDR` | no | Default `:8080` |

## Endpoints

| Route | |
|---|---|
| `GET /docs` | Swagger UI |
| `GET /openapi.yaml` | The OpenAPI document, written by hand in `internal/openapi/openapi.yaml`. A test compares it with the registered routes, so a route and its description go in together |
| `GET /v1/whoami` | Who the credential stands for |
| `/healthz`, `/readyz` (503 while shutting down), `/metrics` | Probes and metrics |

## Authentication

`Authorization: Bearer <credential>`, the credential is either:

- an access token of Authentik (signature against the JWKS of `OIDC_ISSUER`, issuer, audience, expiry, and a verified `email` claim, which is the user);
- an API key `phk_<tenant>_<name>_<secret>`, looked up as the `ApiKey` object `<name>` of the namespace `t-<tenant>`. `spec.hash` is the SHA-256 in hex of the secret part as written. A key past its `expiresAt`, marked `status.expired` or being deleted is refused.

Whatever is wrong with a credential, the answer is the same `401`; the reason is only logged. When Authentik or the Kubernetes API cannot be reached the answer is `503`, not `401`.

```bash
# a token from a terminal, with the device code flow of Authentik
curl -s https://auth.phorge.fr/application/o/device/ -d client_id=<OIDC_AUDIENCE> -d 'scope=openid email profile offline_access'
# approve verification_uri_complete in a browser, then poll /application/o/token/ with
# grant_type=urn:ietf:params:oauth:grant-type:device_code and --data-urlencode "device_code=..."
# (the code holds characters that `curl -d` does not encode, Authentik then answers invalid_grant for ever;
# while you have not approved it answers authorization_pending)
```

