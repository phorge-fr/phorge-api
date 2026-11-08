# Third-party notices

phorge-api is licensed under the Apache License 2.0 (see `LICENSE`). It uses these libraries, each under a license that is compatible with it. The list is of the direct dependencies of `go.mod`; each of them has its own dependencies, under licenses of the same families (Apache-2.0, BSD, MIT, ISC, MPL-2.0).

| Library | License |
| --- | --- |
| [github.com/coder/websocket](https://github.com/coder/websocket) | ISC |
| [github.com/coreos/go-oidc](https://github.com/coreos/go-oidc) | Apache-2.0 |
| [github.com/getkin/kin-openapi](https://github.com/getkin/kin-openapi) | MIT |
| [github.com/go-jose/go-jose](https://github.com/go-jose/go-jose) | Apache-2.0 |
| [github.com/prometheus/client_golang](https://github.com/prometheus/client_golang) | Apache-2.0 |
| [k8s.io/apimachinery](https://github.com/kubernetes/apimachinery) | Apache-2.0 |
| [k8s.io/client-go](https://github.com/kubernetes/client-go) | Apache-2.0 |

`github.com/coder/websocket` carries the terminal: it relays the websockets between the console and Incus. Its ISC license is a permissive one, close to the MIT license, and is compatible with Apache-2.0.
