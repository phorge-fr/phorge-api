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
| `PLATFORM_ADMIN_GROUP` | no | Name of the Authentik group (`phorge-platform-admins`) whose members create, resize and delete tenants, read from the `groups` claim of their token. Empty: nobody can |
| `KUBE_CONTEXT` | no | Context of the kubeconfig, outside the cluster only (development). In a pod the service account is used |
| `LISTEN_ADDR` | no | Default `:8080` |
| `INSTANCE_BOOT_GRACE` | yes | How long after a creation or a start a stop is refused (`409`, `starting`): a stop sent while the guest boots is lost and then hangs, and nothing Incus or Crossplane report says when the system inside listens (measured: the address and the interfaces are there before the agent). `0` never refuses |
| `INSTANCE_STUCK_AFTER` | yes | How long a start or a stop may last before it is reported in `problems` |
| `API_KEY_MAX_LIFETIME` | yes | The longest an API key lives: its `expiresAt` is capped to it. `0` caps nothing. A key always has an expiry, whatever the value |
| `API_KEY_CREATION` | yes | Who creates API keys: `platform-admins` or `users` (any person signed in with Authentik: a key never exceeds what its creator holds, so this only decides who gets one). Anyone lists and revokes their own |

Nothing that decides how the API behaves has a default in the code: a value is written in the deployment, where it is seen. The format constants (the `apikey-` prefix of the account of a key, the names of the annotations, how long creating a key waits for Crossplane) stay in the code.
