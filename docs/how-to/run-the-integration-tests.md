---
type: how-to
audience: developer who tests the API against the real platform
verified: 2026-10-28
---

# Run the integration tests

These tests need the real cluster and the OpenFGA store, and are skipped unless you give them. The tenant tests name their tenant `apitest-api-<random>` and delete it, whatever happens.

## Before you start

- A kubeconfig with the `core` and `control` contexts, and permission to read the `phorge-api-openfga` secret on `control`.
- Never touch `e2e-*` tenants, which belong to the platform suite.

## Authorization against the real model

```bash
kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
export PHORGE_FGA_URL=http://127.0.0.1:18080 PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
go test -run TestAgainstTheRealModel ./internal/authz
```

## The life of a tenant (about five minutes)

```bash
kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
export PHORGE_KUBE_CONTEXT=control PHORGE_FGA_URL=http://127.0.0.1:18080 PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
go test -run TestTenantLifecycle -timeout 20m ./internal/api
```

## Images and instances (ten to twenty minutes)

Set the variables of the previous section, then:

```bash
go test -run TestImageAndInstance -timeout 40m ./internal/api
```

## Memberships and API keys (about five minutes)

Set the same variables, then:

```bash
go test -run TestMembershipsAndKeys -timeout 30m ./internal/api
```

## Related

- [How the API authorizes a request](../explanation/authorization.md)
