---
type: reference
audience: developer or operator who works with images and virtual machines through the API
verified: 2026-10-28
---

# Images and instances

Both live in the namespace `t-<tenant>` of the `phorge.fr` group; the platform administrators may act on any tenant that exists.

- **An instance starts from an image of its tenant**, never from a bare image name: the API refuses an image that does not exist in the tenant (`422`) and one that is not `Ready` yet (`409`, `image_not_ready`). While it is pulled (a few minutes) an instance would report problems that are not one and be called `Failed` after three minutes.
- **Images** are pulled from the `images` server (linuxcontainers.org) only, as virtual machines for the architecture of the compute nodes. The Image kind is the only gate to the image servers, so the API restricts the remote. An image cannot be changed, only deleted and pulled again.
- **Deleting an image** that instances start from is a `409` that names them. Delete them first.
- **Instances**: `cpu` 1 to 16, `memoryMiB` 256 to 65536, `diskGiB` 1 to 500, the rest is the defaults of the XRD (what is not given is not sent). `PATCH` changes `running` (start and stop keep the same VM), `cpu`, `memoryMiB`, `diskGiB` and `sshPublicKeys`, applied live. The image, `nesting` and `secureBoot` cannot change: delete and create.
- **The quota of the plan is Incus's**: a refusal such as `Reached maximum number of instances` comes back as the `problems` of the instance, which becomes `Failed` after three minutes. Stopped VMs count. An instance whose first start failed is stuck: delete it and create it again.
- **SSH keys are checked strictly.** The composition writes them into the cloud-init of the VM with a plain `printf`, one `- <key>` line per key, indented by two spaces, and nothing else protects it: a line break in a key would add a line of cloud-init, a command run as root in the VM. A key is accepted only as `<type> <base64>` and at most a one word comment of letters, digits and `@._+=/-`.
