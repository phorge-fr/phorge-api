---
type: reference
audience: operator who deploys or configures the API
verified: 2026-11-06
---

# Configuration

Configuration is read from the environment and the process refuses to start, listing everything wrong, if a required value is missing:

| Variable | Required | Meaning |
|---|---|---|
| `OIDC_ISSUER` | yes | Authentik issuer, the JWKS is found from it |
| `OIDC_AUDIENCE` | yes | Client id of the Phorge application, or several client ids separated by commas (`client-a,client-b`): the API accepts a token issued for any of them and refuses every other audience. Spaces around an id are ignored and an empty entry stops the start |
| `OPENFGA_URL` | yes | Base URL of OpenFGA |
| `OPENFGA_API_KEY` | yes | Bearer key for OpenFGA (never logged) |
| `OPENFGA_STORE_ID` | yes | Id of the `phorge-api` store |
| `CA_FILE` | no | PEM bundle added to the trusted roots (the internal CA) |
| `KUBE_CONTEXT` | no | Context of the kubeconfig, outside the cluster only (development). In a pod the service account is used |
| `LISTEN_ADDR` | no | Default `:8080` |
| `INSTANCE_BOOT_GRACE` | yes | How long after a creation or a start a stop is refused (`409`, `starting`): a stop sent while the guest boots is lost and then hangs, and nothing Incus or Crossplane report says when the system inside listens (measured: the address and the interfaces are there before the agent). `0` never refuses |
| `INSTANCE_STUCK_AFTER` | yes | How long a start or a stop may last before it is reported in `problems` |
| `API_KEY_MAX_LIFETIME` | yes | The longest an API key lives: its `expiresAt` is capped to it. `0` caps nothing. A key always has an expiry, whatever the value |
| `MAX_PROFILES_PER_TENANT` | yes | How many profiles the users of a tenant may make (the three every tenant has do not count). Over it the answer is `409` with the code `quota_exceeded`. `0` is no limit |
| `MAX_TENANTS_PER_USER` | yes | How many tenants a person may own, whoever created them. Any person signed in with Authentik creates their own, with the `small` plan. Over it the answer is `409` with the code `quota_exceeded`. `0` is no limit |
| `RATE_LIMIT_WRITES_PER_MINUTE` | yes | How many requests that create, change or delete one caller (a person or a key) may send a minute. Over it the answer is `429` with `Retry-After`. `0` limits nothing. It is kept in the memory of one replica: with several, a caller gets the limit of each |
| `RATE_LIMIT_BURST` | yes | How many such requests a caller may send at once before the rate applies, at least `1` |
| `MAX_IMAGES_PER_TENANT` | yes | How many images a tenant may hold. Over it the answer is `409` with the code `quota_exceeded`. `0` is no limit |

Nothing that decides how the API behaves has a default in the code: a value is written in the deployment, where it is seen. The format constants (the `apikey-` prefix of the account of a key, the names of the annotations, how long creating a key waits for Crossplane) stay in the code.
