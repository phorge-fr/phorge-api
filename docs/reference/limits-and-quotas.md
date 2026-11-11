---
type: reference
audience: developer or operator who needs to know what a caller or a tenant may do, and how the API says no
verified: 2026-11-11
---

# Limits and quotas

Four things bound what one caller or one tenant can create. Each has its own answer.

| What | Where it is decided | Answer when it is passed |
|---|---|---|
| What a tenant holds: instances, vCPU, memory, disk, networks | The same limits for every tenant, which Incus enforces as limits of the tenant's project and which the API checks first | `409`, code `quota_exceeded`, a message that says which limit |
| How many profiles its users make | The same limits for every tenant, checked by the API (Incus has no limit for it) | `409`, code `quota_exceeded`, a message that says which limit |
| How many images a tenant holds | `MAX_IMAGES_PER_TENANT`, checked by the API | `409`, code `quota_exceeded` |
| How fast one caller writes | `RATE_LIMIT_WRITES_PER_MINUTE` and `RATE_LIMIT_BURST`, checked by the API | `429`, code `rate_limited`, header `Retry-After` in seconds |

## What a tenant may hold

There is one set of limits and every tenant has it:

| Instances | vCPU | Memory | Disk | Networks | Profiles |
|---|---|---|---|---|---|
| 2 | 2 | 4 GiB | 128 GiB | 2 | 2 |

The CPU, the memory and the disk hold two instances of the profile `s`, or one `m`. The profiles are the ones the users of the tenant make: `s`, `m` and `l` are created with every tenant, nobody changes them, and they are not counted. The disk of the Incus project is 128 GiB plus 512 MiB for each of the 2 VMs (the state of a VM), 132096 MiB; the API checks the 128 GiB.

`GET /v1/tenants/{t}/quota` answers the limits and what the tenant holds (`used`), with the profiles in both. A stopped instance counts, as it does for Incus; an instance that has a profile counts what the profile comes to, one of the three every tenant has or one its users made. `GET /v1/limits` says the same number for the profiles (`profile.maxPerTenant`, 2). The numbers are written twice, here (`internal/quota`) and in the Tenant composition of Hangar; a test of each side says the same numbers.

Creating an instance, a network or a profile that would pass a limit is refused by the API before Incus is asked, and the message says which and what to do: `this tenant has 2 instances out of 2 allowed: delete one first`, `this tenant has 2 networks out of 2 allowed: delete one first`, `this tenant has 2 profiles out of 2 allowed (s, m and l do not count): delete one first`, `this instance needs 200 GiB of disk and the tenant has 0 out of 128 GiB allowed`. The count of instances comes first, then the CPU, the memory and the disk. Incus stays the authority: a request that the API lets through can still be refused there (a `PATCH` that grows an instance is only checked by Incus), and its refusal is told in words in `problems` of the object. An object that is being deleted still counts, as it does for Incus, until it is gone.

A profile bigger than what is left is refused with the limit it passes when an instance takes it: a `l`, or a third `s`.

### The plan is deprecated

There used to be three plans (`small`, `medium`, `large`) that set these limits, and only the platform administrators changed them. There is no choice of plan any more: the `plan` of a tenant limits nothing and changing it changes no limit. It is still in the body of a tenant and in the answer of the quota, and a plan other than `small` is refused with `403` on creation and on change, as before; a tenant is created without one. It stays only until the console stops reading it, then it is removed from the API. An existing tenant has the same limits as a new one, whatever plan it was created with.

## The rate

Requests that create, change or delete (`POST`, `PATCH`, `DELETE`) are counted per caller, a person or a key: `RATE_LIMIT_BURST` of them at once, then `RATE_LIMIT_WRITES_PER_MINUTE` a minute. Reading is not limited, and a caller nobody knows (no credential, a bad one) is refused with `401` and never counted. The count is in the memory of one replica of the API: with several, a caller gets the limit of each, which still bounds it. `0` limits nothing.

## The images

A tenant holds at most `MAX_IMAGES_PER_TENANT` images (`0` is no limit): an image is a pull of a few hundred megabytes into the shared pool.
