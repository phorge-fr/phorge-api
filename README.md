# phorge-api

The Phorge API: authenticated users and API keys manage tenants, images, instances, memberships and API keys.
It creates `phorge.fr` objects on the `control` cluster (see `phorge-fr/Hangar`, `docs/phorge-api.md`), Crossplane turns them into Incus and OpenFGA objects.

Status: step 5 (memberships and API keys) on top of step 4 (images and instances), step 3 (tenants), step 2 (authorization with OpenFGA), step 1 (authentication, OpenAPI document and Swagger UI) and step 0 (configuration, health, metrics, JSON logs, CI). The plan is `docs/phorge-api-implementation.md` in Hangar; `CLAUDE.md` tells an implementer how to start.

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
| `INSTANCE_BOOT_GRACE` | yes | How long after a creation or a start a stop is refused (`409`, `starting`): a stop sent while the guest boots is lost and then hangs, and nothing Incus or Crossplane report says when the system inside listens (measured: the address and the interfaces are there before the agent). `0` never refuses |
| `INSTANCE_STUCK_AFTER` | yes | How long a start or a stop may last before it is reported in `problems` |
| `API_KEY_MAX_LIFETIME` | yes | The longest an API key lives. Positive: an expiry is required and capped to it. `0`: a key may be permanent and its expiry is optional |
| `API_KEY_CREATION` | yes | Who creates and changes API keys: `platform-admins` or `users` (any person signed in with Authentik: a key never exceeds its owner, so this only decides who gets one). Anyone lists and revokes their own |
| `AUTHENTIK_URL`, `AUTHENTIK_TOKEN` | together | Where the API asks whether the owner of a key is a platform administrator, and the token that may read that group |
| `AUTHENTIK_CACHE_TTL`, `AUTHENTIK_TIMEOUT` | with `AUTHENTIK_URL` | How long the members of the group are remembered (also how long someone removed from it keeps what it gave to their keys), and how long a request to Authentik may last |

Nothing that decides how the API behaves has a default in the code: a value is written in the deployment, where it is seen. The format constants (the `phk_` prefix, the size of a secret, the names of the annotations) stay in the code.

## Endpoints

| Route | |
|---|---|
| `GET /docs` | Swagger UI |
| `GET /openapi.yaml` | The OpenAPI document, written by hand in `internal/openapi/openapi.yaml`. A test compares it with the registered routes, so a route and its description go in together |
| `GET /v1/whoami` | Who the credential stands for |
| `GET /v1/tenants`, `GET /v1/tenants/{t}` | The tenants you can see (the platform administrators see all of them) |
| `POST /v1/tenants`, `PATCH /v1/tenants/{t}`, `DELETE /v1/tenants/{t}` | Create, resize and delete a tenant: platform administrators only |
| `GET`, `POST /v1/tenants/{t}/images`, `GET`, `DELETE /v1/tenants/{t}/images/{name}` | The images of a tenant: read with `images:read` or a role, write with `operator` or `images:write` |
| `GET`, `POST /v1/tenants/{t}/memberships`, `GET`, `DELETE /v1/tenants/{t}/memberships/{name}` | The roles of the users: admins of the tenant (a key of one, with `memberships:read` and `memberships:write`) |
| `GET`, `POST /v1/apikeys`, `GET`, `PATCH`, `DELETE /v1/apikeys/{id}` | Your own API keys, with a token of Authentik (a key cannot manage keys). Creating and changing: platform administrators for now |
| `GET`, `POST /v1/tenants/{t}/instances`, `GET`, `PATCH`, `DELETE /v1/tenants/{t}/instances/{name}` | The virtual machines: read with `instances:read` or a role, write with `user` or `instances:write` |
| `/healthz`, `/readyz` (503 while shutting down), `/metrics` | Probes and metrics |

## Authentication

`Authorization: Bearer <credential>`, the credential is either:

- an access token of Authentik (signature against the JWKS of `OIDC_ISSUER`, issuer, audience, expiry, and a verified `email` claim, which is the user);
- a personal API key `phk_<id>_<secret>`, looked up as the `ApiKey` object `<id>` of the namespace `phorge-keys`. `spec.hash` is the SHA-256 in hex of the secret part as written. A key past its `expiresAt`, marked `status.expired`, being deleted or without an owner is refused. A key is **its owner**: the identity is `user:<owner>`, limited to the scopes of the key.

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

## Images and instances

Both live in the namespace `t-<tenant>` of the `phorge.fr` group; the platform administrators may act on any tenant that exists.

- **An instance starts from an image of its tenant**, never from a bare image name: the API refuses an image that does not exist in the tenant (`422`) and one that is not `Ready` yet (`409`, `image_not_ready`). While it is pulled (a few minutes) an instance would report problems that are not one and be called `Failed` after three minutes.
- **Images** are pulled from the `images` server (linuxcontainers.org) only, as virtual machines for the architecture of the compute nodes. The Image kind is the only gate to the image servers, so the API restricts the remote. An image cannot be changed, only deleted and pulled again.
- **Deleting an image** that instances start from is a `409` that names them. Delete them first.
- **Instances**: `cpu` 1 to 16, `memoryMiB` 256 to 65536, `diskGiB` 1 to 500, the rest is the defaults of the XRD (what is not given is not sent). `PATCH` changes `running` (start and stop keep the same VM), `cpu`, `memoryMiB`, `diskGiB` and `sshPublicKeys`, applied live. The image, `nesting` and `secureBoot` cannot change: delete and create.
- **The quota of the plan is Incus's**: a refusal such as `Reached maximum number of instances` comes back as the `problems` of the instance, which becomes `Failed` after three minutes. Stopped VMs count. An instance whose first start failed is stuck: delete it and create it again.
- **SSH keys are checked strictly.** The composition writes them into the cloud-init of the VM with a plain `printf`, one `  - <key>` per line, and nothing else protects it: a line break in a key would add a line of cloud-init, a command run as root in the VM. A key is accepted only as `<type> <base64>` and at most a one word comment of letters, digits and `@._+=/-`.

The test that pulls a real image and boots a real VM takes ten to twenty minutes (see the tenants section for the variables):

```bash
go test -run TestImageAndInstance -timeout 40m ./internal/api
```

## Memberships and API keys

- **A membership gives a user a role** on a tenant (`admin`, `operator`, `user`, `viewer`). Memberships are objects of the tenant's namespace, read and written only by the admins of the tenant (the relations `can_read_memberships` and `can_write_memberships`) or a platform administrator. A membership cannot be edited, because a role is an OpenFGA tuple that cannot be changed in place: to change a role, delete the membership and create another. There is one per user and tenant: its name is derived from the email, so a second one is a `409`. The owner of a tenant already is admin through the tenant and a membership for it is refused (`409`, `owner_is_admin`): to hand a tenant over, give someone else the `admin` role. Only users are supported, a group is refused (nothing writes the tuples a group needs).
- **Deleting a membership is not an instant revocation**: the object goes at once, its tuples within about a minute (measured on the cluster in the test below).
- **An API key is personal**: it belongs to the user who created it, not to a tenant, and acts as them within its scopes (`tenants:read`, `tenants:write`, `instances:read|write`, `images:read|write`, `memberships:read|write`). It is `phk_<id>_<secret>`: the id locates the object with one get, the secret is 32 random bytes in hex. The answer to its creation holds the key, **once**; only the SHA-256 of the secret is stored (`spec.hash`), so no read ever shows it again.
- **Only a person manages keys**, signed in with Authentik: a key cannot create, read or delete keys. `API_KEY_CREATION` says who creates and changes them (the platform administrators, or any person); anyone lists and revokes their own (a key can always be revoked). Someone else's key answers `404`.
- **A key never exceeds its owner**, because the owner is what is asked at every request. A key expires: with `API_KEY_MAX_LIFETIME` set, `expiresAt` is required and capped to it.
- **A key stops at its `expiresAt` to the second**: the authentication checks the date itself and does not wait for the composition. A key that is being deleted stops at once too. The list shows `expired` as soon as the date has passed.
- A key cannot be rotated: delete it and create another.

The test of all this on the real cluster (a viewer, a user, a revoked member, a platform administrator's key within its scopes, a key that stops at its date and at its deletion) takes about five minutes:

```bash
go test -run TestMembershipsAndKeys -timeout 30m ./internal/api
```

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from the commit messages (Conventional Commits). On every push to `main` it keeps one pull request open, "chore(main): release x.y.z", that bumps `.release-please-manifest.json` and writes `CHANGELOG.md`. Merging that pull request is the release: it creates the tag and the GitHub release, and the tag builds and pushes the image `ghcr.io/phorge-fr/phorge-api:x.y.z`. Nothing is released until you merge it.

- `fix:` gives a patch, `feat:` a minor (while the version is below 1.0.0, a breaking change `feat!:` also gives a minor), `chore:`, `docs:`, `test:` and `ci:` give nothing.
- Hangar pins the image as `<tag>@<digest>`: Renovate opens the pull request there when a release is published.
- The workflow uses the `RENOVATE_TOKEN` secret, because pull requests and tags created with `GITHUB_TOKEN` start no workflow, so the checks of the release pull request and the image build would never run.

## Contributing

Every change starts from an issue and goes through a pull request; the rules (issue link, label, milestone, assignee, reviewer) are in the organization's [GOVERNANCE](https://github.com/phorge-fr/.github/blob/main/GOVERNANCE.md) and [CONTRIBUTING](https://github.com/phorge-fr/.github/blob/main/CONTRIBUTING.md). Planned work is on the public [Roadmap](https://github.com/orgs/phorge-fr/projects/3). Everyone taking part follows the [Code of Conduct](https://github.com/phorge-fr/.github/blob/main/CODE_OF_CONDUCT.md).

## Security

Report vulnerabilities privately, as described in the [security policy](https://github.com/phorge-fr/.github/blob/main/SECURITY.md). Do not open a public issue for them.

## License

Licensed under the [Apache License 2.0](LICENSE).
