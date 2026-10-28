---
type: reference
audience: developer or operator who works with tenants through the API
verified: 2026-10-28
---

# Tenants

A tenant is a `Tenant` object of the `phorge.fr` group in `phorge-system` (`internal/kube/tenants.go`); Crossplane makes its namespace `t-<name>`, its Incus project and its network.

- **Create** answers `202` with a `Location`: poll it until `phase` is `Ready`. `phase` is `Provisioning`, `Ready`, `Deleting`, or `Failed` when the tenant has not become ready for 3 minutes and `problems` holds the error of Incus or OpenFGA, shown as it is.
- **Patch** changes `plan` and `displayName`. The owner cannot change (it is an OpenFGA tuple): a body that holds it is refused. To hand a tenant over, give the new person the `admin` role with a membership.
- **Delete** is done in the foreground: `202`, then the tenant stays as `Deleting` until everything it is made of is gone (about two minutes), and a `404` means it is really gone. It destroys the virtual machines of the tenant.
- Whoever cannot read a tenant is told it does not exist (`404`), whoever can but may not do this gets `403`. Bodies are JSON only, 64 KiB at most, with no field the API does not know.
