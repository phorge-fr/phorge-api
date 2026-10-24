# phorge-api

The Phorge API: authenticated users and API keys manage tenants, images, instances, memberships and API keys.
It creates `phorge.fr` objects on the `control` cluster (see `phorge-fr/Hangar`, `docs/phorge-api.md`), Crossplane turns them into Incus and OpenFGA objects.

Status: step 3 (tenants) on top of step 2 (authorization with OpenFGA), step 1 (authentication, OpenAPI document and Swagger UI) and step 0 (configuration, health, metrics, JSON logs, CI). The plan is `docs/phorge-api-implementation.md` in Hangar; `CLAUDE.md` tells an implementer how to start.

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
| `PLATFORM_ADMIN_GROUP` | no | Name of the Authentik group (`phorge-platform-admins`) whose members create, resize and delete tenants, read from the `groups` claim of their token. Empty: nobody can |
| `KUBE_CONTEXT` | no | Context of the kubeconfig, outside the cluster only (development). In a pod the service account is used |
| `LISTEN_ADDR` | no | Default `:8080` |

## Endpoints

| Route | |
|---|---|
| `GET /docs` | Swagger UI |
| `GET /openapi.yaml` | The OpenAPI document, written by hand in `internal/openapi/openapi.yaml`. A test compares it with the registered routes, so a route and its description go in together |
| `GET /v1/whoami` | Who the credential stands for |
| `GET /v1/tenants`, `GET /v1/tenants/{t}` | The tenants you can see (the platform administrators see all of them) |
| `POST /v1/tenants`, `PATCH /v1/tenants/{t}`, `DELETE /v1/tenants/{t}` | Create, resize and delete a tenant: platform administrators only |
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


## Authorization

Authentication tells who calls, OpenFGA decides what they may do (`internal/authz`). Every decision is a `Check` in the `phorge-api` store on the object `tenant:<name>`, for the subject of the identity (`user:<email>` or `apikey:<tenant>.<name>`):

| Action | Relation | Scope of a key |
|---|---|---|
| read the tenant | `can_read_tenant` | `tenant:read` |
| read instances, images | `can_read_instances`, `can_read_images` | `instances:read`, `images:read` |
| write instances | `can_write_instances` | `instances:write` |
| write images | `can_write_images` | `images:write` |
| read, write memberships and keys | `can_read_memberships`, `can_write_memberships` | `memberships:read`, `memberships:write` |

A user gets those through a role (`admin` over `operator` over `user` over `viewer`), a key only through its scopes, which are relations of their own (`instances:write` does not give `instances:read`). `Authorizer.Require(action, ...)` is the middleware of a route: `404` when the caller cannot read the tenant (a tenant that is not yours is not shown to exist), `403` when it can but may not do this, `503` when OpenFGA cannot answer. A decision that cannot be taken is never an "allowed". `Authorizer.Missing` is what keeps a key within its creator, `Authorizer.IsPlatformAdmin` the people who create tenants: the members of the Authentik group `PLATFORM_ADMIN_GROUP` (the model has no relation for it); `GET /v1/whoami` says whether the caller is one. Members are added in Authentik, the group itself is created by the blueprint in Hangar.

OpenFGA is reached with `OPENFGA_URL`, `OPENFGA_API_KEY` and `OPENFGA_STORE_ID`, and `CA_FILE` for the internal CA of `openfga.core.phorge` (the verification is never turned off).

The test against the real model is skipped unless you give it a store; it writes the tuples of a tenant of its own and removes them:

```bash
kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
export PHORGE_FGA_URL=http://127.0.0.1:18080 PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
go test -run TestAgainstTheRealModel ./internal/authz
```

## Tenants

A tenant is a `Tenant` object of the `phorge.fr` group in `phorge-system` (`internal/kube/tenants.go`); Crossplane makes its namespace `t-<name>`, its Incus project and its network.

- **Create** answers `202` with a `Location`: poll it until `phase` is `Ready`. `phase` is `Provisioning`, `Ready`, `Deleting`, or `Failed` when the tenant has not become ready for 3 minutes and `problems` holds the error of Incus or OpenFGA, shown as it is.
- **Patch** changes `plan` and `displayName`. The owner cannot change (it is an OpenFGA tuple): a body that holds it is refused. To hand a tenant over, give the new person the `admin` role with a membership.
- **Delete** is done in the foreground: `202`, then the tenant stays as `Deleting` until everything it is made of is gone (about two minutes), and a `404` means it is really gone. It destroys the virtual machines of the tenant.
- Whoever cannot read a tenant is told it does not exist (`404`), whoever can but may not do this gets `403`. Bodies are JSON only, 64 KiB at most, with no field the API does not know.

The test of the whole life of a tenant on the real cluster takes about five minutes and is skipped unless you give it the cluster and the store (it names its tenant `apitest-api-<random>` and deletes it, whatever happens):

```bash
kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
export PHORGE_KUBE_CONTEXT=control PHORGE_FGA_URL=http://127.0.0.1:18080 PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
go test -run TestTenantLifecycle -timeout 20m ./internal/api
```
