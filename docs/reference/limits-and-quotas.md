---
type: reference
audience: developer or operator who needs to know what a caller or a tenant may do, and how the API says no
verified: 2026-11-08
---

# Limits and quotas

Three things bound what one caller or one tenant can create. Each has its own answer.

| What | Where it is decided | Answer when it is passed |
|---|---|---|
| What a tenant holds: instances, vCPU, memory, disk, networks | The plan of the tenant (`small`, `medium`, `large`), which Incus enforces as limits of the tenant's project, and which the API checks first | `409`, code `quota_exceeded`, a message that says which limit |
| How many images a tenant holds | `MAX_IMAGES_PER_TENANT`, checked by the API | `409`, code `quota_exceeded` |
| How many profiles its users make | `MAX_PROFILES_PER_TENANT`, checked by the API | `409`, code `quota_exceeded` |
| How fast one caller writes | `RATE_LIMIT_WRITES_PER_MINUTE` and `RATE_LIMIT_BURST`, checked by the API | `429`, code `rate_limited`, header `Retry-After` in seconds |

## The plan of a tenant

| Plan | Instances | vCPU | Memory | Disk | Networks |
|---|---|---|---|---|---|
| `small` | 2 | 2 | 4 GiB | 128 GiB | 1 |
| `medium` | 4 | 4 | 8 GiB | 256 GiB | 2 |
| `large` | 6 | 8 | 16 GiB | 512 GiB | 3 |

The CPU, the memory and the disk of each plan hold two instances of its own profile size: `small` two `s`, `medium` two `m`, `large` two `l`. The count of instances is the plan's own (2, 4 and 6), for instances smaller than the profile.

`GET /v1/tenants/{t}/quota` answers the plan, the limits and what the tenant holds (`used`). A stopped instance counts, as it does for Incus; an instance that has a profile counts what the profile comes to, one of the three every tenant has or one its users made. The table is written twice, here and in the Tenant composition of Hangar; a test of each side says the same numbers.

Creating an instance or a network that would pass a limit is refused by the API before Incus is asked, and the message says which: `this tenant has 2 instances out of 2 allowed by its plan small: delete one first`, `this instance needs 200 GiB of disk and the tenant has 0 out of 128 GiB allowed by its plan small`. The count comes first, then the CPU, the memory and the disk. Incus stays the authority: a request that the API lets through can still be refused there (a `PATCH` that grows an instance is only checked by Incus), and its refusal is told in words in `problems` of the object.

The plan of a tenant is not changed through the API: the platform administrators raise the limits of a tenant on request, on its `Tenant` object.

A profile bigger than what is left of a plan is refused with the limit it passes: a `l` on a `small` plan, or a third `s` on it. The numbers of the plans are written twice, in the Tenant composition of Hangar and in `internal/quota`, and a test of each says the same.

## The rate

Requests that create, change or delete (`POST`, `PATCH`, `DELETE`) are counted per caller, a person or a key: `RATE_LIMIT_BURST` of them at once, then `RATE_LIMIT_WRITES_PER_MINUTE` a minute. Reading is not limited, and a caller nobody knows (no credential, a bad one) is refused with `401` and never counted. The count is in the memory of one replica of the API: with several, a caller gets the limit of each, which still bounds it. `0` limits nothing.

## The images

A tenant holds at most `MAX_IMAGES_PER_TENANT` images (`0` is no limit): an image is a pull of a few hundred megabytes into the shared pool.
