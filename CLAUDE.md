# Phorge API: instructions for the coding agent

This repository is the Phorge API: a Go REST API that manages tenants and their VMs by creating, reading and deleting
Kubernetes objects of the `phorge.fr/v1alpha1` group on the `control` cluster. Crossplane does the rest.

## Read this first, before writing code

The contract and the plan are in the **Hangar** repository (`phorge-fr/Hangar`, GitOps of the clusters). Read both pages in full:

```bash
gh api repos/phorge-fr/Hangar/contents/docs/phorge-api.md -H 'Accept: application/vnd.github.raw'
gh api repos/phorge-fr/Hangar/contents/docs/phorge-api-implementation.md -H 'Accept: application/vnd.github.raw'
```

`phorge-api.md` describes the five kinds (`Tenant`, `Image`, `Instance`, `Membership`, `ApiKey`). `phorge-api-implementation.md` is
your plan: what exists, how to reach it from your machine, how authentication and authorization work, what the platform does
(and which behaviours you must build around), the order of work in seven steps, and the rules. Follow its order. The XRDs in
`overlays/control/controllers/crossplane/` of Hangar are the source of truth for field names and limits; if a page contradicts
the cluster, the cluster is right.

You are at **step 0** of that plan: this directory only contains a skeleton (health endpoints and a clean shutdown).

## Rules

- Never add Claude or Anthropic attribution to a commit message or a pull request. Issues and pull request descriptions in English.
- Ask the owner before any `git commit` or `git push`. Do not push to `main`; work on a branch and open a pull request per step.
- Never print, log or commit a secret, a key or a token. Configuration comes from the environment.
- Do not change the platform by hand. An XRD, a composition, the OpenFGA model, RBAC, a firewall rule or an Authentik blueprint is a pull request on
  Hangar (or the Network repository), never an edit on the cluster. Flux reverts live edits.
- Your test tenants are named `apitest-<run>-*`. Never touch `e2e-*` tenants (the platform suite's), the Incus projects `ct-poc` and `default`, or a
  tenant you did not create. Clean up what you create: children first, then the tenant, then wait until the Incus project is gone.
- When something is missing or ambiguous (public host names, registry and CI, who may create tenants), ask; do not invent.

## Commands

```bash
make vet test build        # what CI runs
docker build -t phorge-api .
```

The Go version is the one in `go.mod`.
