---
type: reference
audience: developer or operator who works with tenants through the API
verified: 2026-11-08
---

# Tenants

A tenant is a `Tenant` object of the `phorge.fr` group in `phorge-system` (`internal/kube/tenants.go`); Crossplane makes its namespace `t-<name>`, its Incus project, and its network.

- **Create** is for the platform administrators, who choose the owner and the plan, and, when `TENANT_CREATION` is `users`, for any person for themselves: the owner is the caller (`owner` is left out or is their own email, another one is `403`), the plan is `small` (another one is `403`), and a person owns at most `MAX_TENANTS_PER_USER` tenants (`409`, `quota_exceeded`, counted by owner, whoever created them). A key never creates a tenant. `GET /v1/whoami` says `canCreateTenants`. It answers `202` with a `Location`: poll it until `phase` is `Ready`. `phase` is `Provisioning`, `Ready`, `Deleting`, or `Failed` when the tenant has not become ready for 3 minutes and `problems` says what is wrong in words made for the user (a limit of the plan that is reached, or that the platform could not complete a step yet). It never carries what Crossplane, a provider or Incus report: those stay in `status.problems` of the object, for the administrators.
- **Patch** changes `plan` and `displayName`: the admins of the tenant, the owner among them, change the display name, and only a platform administrator changes the plan (`403` otherwise). A key never does. The owner cannot change (it is an OpenFGA tuple): a body that holds it is refused. To hand a tenant over, give the new person the `admin` role with a membership.
- **Delete** is for the admins of the tenant, the owner among them, and the platform administrators. It removes what the tenant holds first (its instances, then its memberships, then its networks, then the profiles its users made, then its images), then the tenant, all in the foreground: `202`, then the tenant stays as `Deleting` until everything it is made of is gone (about two minutes, longer with a virtual machine to stop), and a `404` means it is really gone. It destroys the virtual machines of the tenant. The order is not cosmetic: the platform deletes what a tenant holds in its namespace only while the namespace exists, so the tenant waits for them (see the Hangar issue 185).
- Whoever cannot read a tenant is told it does not exist (`404`), whoever can but may not do this gets `403`. Bodies are JSON only, 64 KiB at most, with no field the API does not know.
