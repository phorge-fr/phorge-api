---
type: reference
audience: developer or operator who works with images and virtual machines through the API
verified: 2026-11-08
---

# Images and instances

Both live in the namespace `t-<tenant>` of the `phorge.fr` group; the platform administrators may act on any tenant that exists.

- **An instance starts from an image of its tenant**, never from a bare image name: the API refuses an image that does not exist in the tenant (`422`) and one that is not `Ready` yet (`409`, `image_not_ready`). While it is pulled (a few minutes) an instance would report problems that are not one and be called `Failed` after three minutes.
- **Images** are pulled from the `images` server (linuxcontainers.org) only, as virtual machines for the architecture of the compute nodes. The Image kind is the only gate to the image servers, so the API restricts the remote. An image cannot be changed, only deleted, and pulled again.
- **Deleting an image** that instances start from is a `409` that names them. Delete them first.
- **Instances**: `cpu` 1 to 16, `memoryMiB` 256 to 65536, `diskGiB` 1 to 500, the rest is the defaults of the XRD (what is not given is not sent). `PATCH` changes `running` (start and stop keep the same VM), `cpu`, `memoryMiB`, `diskGiB` and `sshPublicKeys`, applied live. The image, `nesting` and `secureBoot` cannot change: delete and create.
- **The quota of the plan is Incus's**: a refusal such as `Reached maximum number of instances` comes back as the `problems` of the instance, in words (`This tenant has reached the maximum number of instances of its plan.`), and the instance becomes `Failed` after three minutes. Stopped VMs count. An instance whose first start failed is stuck: delete it and create it again.
- **SSH keys are checked strictly.** The composition writes them into the cloud-init of the VM with a plain `printf`, one `- <key>` line per key, indented by two spaces, and nothing else protects it: a line break in a key would add a line of cloud-init, a command run as root in the VM. A key is accepted only as `<type> <base64>` and at most a one word comment of letters, digits and `@._+=/-`.
- **An instance is on a network of its tenant** (`network`, see [Networks](networks.md)): named in the request, or the only one of the tenant.
- **The size of an instance is a profile or its own values, never both.** `profile` is one of `GET /v1/tenants/{t}/profiles` (`s` 1 vCPU, 2 GiB, 64 GiB; `m` 2, 4, 128; `l` 4, 8, 256); the other way is `cpu`, `memoryMiB` and `diskGiB`, and what a request leaves out takes 1, 1024 and 10 (the Crossplane kind has no defaults, the API completes the size). A profile with a value is `422`, so is a profile that does not exist, and the answer lists the profiles. The body of an instance carries its `profile` and, in every case, the `cpu`, `memoryMiB` and `diskGiB` it comes to. `GET /v1/limits` has the bounds.
- **A size changes in place**: `PATCH` with a `profile` moves the instance to it and removes its own values; an instance that has a profile leaves it by being given the three values together (a value alone is `422`, it would sit next to the profile). Incus applies it live on a stopped instance.
- **The resources of a plan hold two instances of its own profile size** (`small` two `s`, `medium` two `m`, `large` two `l`), and the count of instances is 2, 4 and 6: an instance that asks for more than what is left is refused with the limit it passes, before Incus is asked (see [Limits and quotas](limits-and-quotas.md)).
- **A user makes their own profiles** (`POST /v1/tenants/{t}/profiles`): CPU, memory and disk, all three given, under a name of 1 to 32 characters (`s`, `m`, `l` and `default` are taken, `422`). They are listed after the three every tenant has, with a phase and what is wrong in words. An instance takes one as it takes `s`, `m` or `l`, and counts in the quota what it comes to. Deleting one is refused (`409`, `in_use`) while an instance has it, and names the instances; `s`, `m` and `l` are not changed nor deleted (`403`). A tenant holds at most `MAX_PROFILES_PER_TENANT` of them. Changing a profile in place is not in the API: make another one and move the instances to it.
