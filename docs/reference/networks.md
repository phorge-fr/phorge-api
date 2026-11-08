---
type: reference
audience: developer or operator who works with the networks of a tenant through the API
verified: 2026-11-08
---

# Networks

A network is a `Network` object of the `phorge.fr` group in the tenant's namespace (`internal/kube/networks.go`). Crossplane makes an Incus OVN network of that name in the tenant's project, NATed on the uplink. **A tenant comes without a network**: a user deploys the ones they need and attaches their instances to them.

- **Create** answers `202` with a `Location`: poll it until `phase` is `Ready`. The name is 1 to 15 characters (the limit of Incus), lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen. The only other field is a `description` of 200 characters at most: the addresses are chosen by Incus and a network is always NATed, so a field for either is refused (`400`).
- **The plan limits how many** (`small` 1, `medium` 2, `large` 3): the next one never becomes ready and `problems` says `This tenant has reached the maximum number of networks of its plan.`
- **Delete** is done in the foreground: `202`, then the network stays as `Deleting` until Incus has deleted it. A network that an instance is attached to is refused with `409` and the names of the instances: delete them first. For a few seconds after the last one is gone, Crossplane still refuses, with a `409` that names nobody; the deletion Crossplane refused is replayed by itself once the last instance is gone.
- **An instance is attached to one network** (`network` on the instance, `eth0` in the VM). When a request to create an instance names none, the only network of the tenant is used; with none the request is refused (`422`, `the tenant has no network: create one first`), with several it is too and the networks are listed. A network that is not `Ready` yet is refused (`409`, `network_not_ready`), as an image is.
- **Who may**: reading is for every role of the tenant (`viewer` and above) or a key with `networks:read`, writing for `operator` and `admin` or a key with `networks:write`.
- Deleting a tenant deletes its networks too, after its instances and before its images, see [Tenants](tenants.md).
