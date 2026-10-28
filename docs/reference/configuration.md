---
type: reference
audience: operator who deploys or configures the API
verified: 2026-10-28
---

# Configuration

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
