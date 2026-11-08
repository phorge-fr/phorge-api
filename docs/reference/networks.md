---
type: reference
audience: developer or operator who works with the networks of a tenant through the API
verified: 2026-11-08
---

# Networks

A network is a `Network` object of the `phorge.fr` group in the tenant's namespace (`internal/kube/networks.go`). Crossplane makes an Incus OVN network of that name in the tenant's project, NATed on the uplink. **A tenant comes without a network**: a user deploys the ones they need and attaches their instances to them.

- **Create** answers `202` with a `Location`: poll it until `phase` is `Ready`. The name is 1 to 15 characters (the limit of Incus), lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen. A network is always NATed, so a field for it is refused (`400`). The other fields are a `description` of 200 characters at most and an optional `ipv4`, see below.
- **The range is the user's choice**: `ipv4` is the address of the network with a prefix, such as `10.77.1.0/24`, in a private range (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), with a prefix from `/16` to `/29`. A host address (`10.77.1.5/24`), a public or IPv6 range or a prefix out of bounds is refused with `422`. The gateway is the first host of the range. Without `ipv4` Incus chooses the range. The range is checked against the platform's own ranges (`range_reserved`, `409`) and against the networks of every tenant (`range_in_use`, `409`, without saying whose): Incus accepts two networks on the same range, and their instances would not tell them apart. The answer carries `ipv4` once the platform knows it, that is for a range the user chose; a range Incus chose is not reported yet.
- **The plan limits how many** (`small` 1, `medium` 2, `large` 3): the next one never becomes ready and `problems` says `This tenant has reached the maximum number of networks of its plan.`
- **Delete** is done in the foreground: `202`, then the network stays as `Deleting` until Incus has deleted it. A network that an instance is attached to is refused with `409` and the names of the instances: delete them first. For a few seconds after the last one is gone, Crossplane still refuses, with a `409` that names nobody; the deletion Crossplane refused is replayed by itself once the last instance is gone.
- **An instance is attached to one network** (`network` on the instance, `eth0` in the VM). When a request to create an instance names none, the only network of the tenant is used; with none the request is refused (`422`, `the tenant has no network: create one first`), with several it is too and the networks are listed. A network that is not `Ready` yet is refused (`409`, `network_not_ready`), as an image is.
- **Who may**: reading is for every role of the tenant (`viewer` and above) or a key with `networks:read`, writing for `operator` and `admin` or a key with `networks:write`.
- Deleting a tenant deletes its networks too, after its instances and before its images, see [Tenants](tenants.md).
