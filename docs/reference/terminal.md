---
type: reference
audience: developer of a client of the API that opens a terminal on an instance
verified: 2026-11-08
---

# Terminal

`GET /v1/tenants/{t}/instances/{name}/console` opens an interactive shell in a running virtual machine, as a websocket. It is the exec of Incus, relayed by the API with the identity of the person ([decision 0013](https://github.com/phorge-fr/Hangar/blob/main/docs/decisions/0013-the-api-exchanges-the-token-of-a-user-for-incus.md)). The API holds no credential of Incus: it trades the token of the person at Authentik for a token of the Incus application, and Incus decides from its own store. The code is in `internal/terminal` and `internal/api/terminal.go`.

## Opening

- An `Upgrade: websocket` request, authenticated by the `Authorization: Bearer` header. **Never a token in the URL**: URLs are logged. A plain `GET` answers `426`.
- For a person signed in with Authentik who holds `instances:write` on the tenant (the roles `user`, `operator` and `admin`). A key gets `403`, even with `instances:write`: a terminal is interactive.
- The query takes `cols` and `rows`, from 1 to 1000, for the first size of the window (80 by 24 otherwise). Anything else is `422`.
- The instance must exist (`404`) and be running as Incus reports it (`409`, code `instance_not_running`): a VM that was asked to run and is not up yet is not running. It needs the Incus agent, so an image of a `cloud` variant.

Before the upgrade the API answers in JSON, as everywhere. Every answer is a sentence for a person, never what Incus or Authentik said.

| Status | Code | When |
|---|---|---|
| `101` | | The terminal is open |
| `403` | `forbidden` | A key; the person is not admitted to the Incus application at Authentik; Incus has no `can_exec` for the person on this instance |
| `404` | `not_found` | No such tenant (or none the person may see), or no such instance |
| `409` | `instance_not_running` | The instance is not running |
| `422` | `invalid` | `cols` or `rows` out of bounds |
| `426` | `upgrade_required` | Not a websocket upgrade |
| `429` | `too_many_terminals`, `rate_limited` | The limit of terminals of the person or of the tenant; too many openings (`Retry-After`) |
| `503` | `unavailable` | Authentik, Incus or the cluster cannot answer |

## What is carried

- **Binary frames** carry the bytes of the terminal in both directions: what is typed, what is shown. Nothing reads them.
- **Text frames** from the client are JSON controls. Today one: `{"type":"resize","cols":120,"rows":40}`, with sizes from 1 to 1000, relayed to Incus as a window resize. A control that is not understood is ignored. There is no mandatory sub-protocol.
- The shell is a login shell of the image (`bash` when there is one, `sh` otherwise), as root, in a VM that is the tenant's.

## Closing

The API closes the connection with a code and a sentence for the person.

| Code | Meaning |
|---|---|
| `1000` | The session ended: the shell exited, or the client closed it |
| `4001` | Idle for longer than `TERMINAL_IDLE_TIMEOUT` |
| `4002` | Open for longer than `TERMINAL_MAX_DURATION` |
| `4003` | The person no longer holds `instances:write` on the tenant |
| `4004` | The instance is no longer running, or no longer exists |
| `4005` | The platform could not check the rights twice in a row |
| `4006` | The platform is restarting |

**Incus does not end a session whose right was removed** (measured: still open 92 seconds after). The API checks the right and the state of the instance every `TERMINAL_RECHECK_INTERVAL` and closes the session itself. When the client closes, Incus ends the process.

## Limits

A person and a tenant have a limited number of open terminals, and a terminal is closed after a time without a byte and after a maximum duration. All of them, and the rate of openings (the one of the writes), are settings of the deployment, see [Configuration](configuration.md).

## Audit

The log says who opened a terminal on which instance, in which tenant, with which window, and later how it ended and after how many seconds (`terminal opened`, `terminal closed` with `reason`). **Never what was typed or shown**, and never a token.

## Who sees the token

The token of the person goes only to Authentik, for the exchange. The token the exchange returns goes only to Incus, for that terminal, and is kept in memory for the opening. Neither is stored nor logged.
