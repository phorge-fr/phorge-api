---
type: how-to
audience: developer of the console, or of any client that gives a person a terminal on their instance
verified: 2026-11-08
---

# Open a terminal from the console

A browser never holds the token. The browser talks to the console over a websocket authenticated by the session cookie, and the console talks to the API over a second websocket authenticated by `Authorization: Bearer`. The console only relays the frames between the two. The rules are the API's, not the console's: the console may hide the button, the API refuses.

## Steps

1. Show the button only for a running instance, to a person who may write instances. These are comfort checks: the answer of the API is the real one.
2. On the server of the console, open a websocket to `GET /v1/tenants/{t}/instances/{name}/console?cols=N&rows=M` with the header `Authorization: Bearer <the token of the person>`. Pass the size of the terminal in the query.
3. If the handshake answers a status other than `101`, show the `error.message` of the JSON body as it is. `409` means the instance is not running, `429` too many terminals, `403` the person may not.
4. Relay in both directions: a binary frame from the browser is typed text, a binary frame from the API is shown text. Do not read them, keep them or log them.
5. When the window changes size, send a text frame `{"type":"resize","cols":N,"rows":M}` to the API.
6. When the API closes the connection, close the browser side with the same code and show the reason of the close frame. The codes are in [Terminal](../reference/terminal.md).
7. When the browser leaves, close the connection to the API: Incus then ends the shell.

## Check

Open a terminal on a running instance of your tenant, type a command, resize the window and close it. Then remove your role in another window and watch the terminal close by itself within the recheck interval, with the code `4003`.

## Undo

Nothing to undo: closing the websocket ends the session on both sides.
