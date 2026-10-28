# AGENTS.md: phorge-api

Instructions for anyone working in this repository, human or AI assistant. The organization-wide rules (issues, pull requests, time tracking, security reporting) are in [phorge-fr/.github](https://github.com/phorge-fr/.github/blob/main/AGENTS.md) and [GOVERNANCE.md](https://github.com/phorge-fr/.github/blob/main/GOVERNANCE.md); this file only adds what is specific to phorge-api.

## What this repository is

The Phorge API: a Go REST API that manages tenants and their VMs by creating, reading and deleting Kubernetes objects of the `phorge.fr/v1alpha1` group (`Tenant`, `Image`, `Instance`, `Membership`, `ApiKey`) on the `control` cluster. Crossplane does the rest. The code lives here; the platform it runs on lives in `phorge-fr/Hangar`.

## Read this first, before writing code

The contract and the plan are in Hangar. Read both pages in full:

```bash
gh api repos/phorge-fr/Hangar/contents/docs/reference/phorge-api-kinds.md -H 'Accept: application/vnd.github.raw'
gh api repos/phorge-fr/Hangar/contents/docs/explanation/phorge-api-implementation-plan.md -H 'Accept: application/vnd.github.raw'
```

`phorge-api-kinds.md` describes the five kinds. `phorge-api-implementation-plan.md` is the plan: how to reach the platform from your machine, how authentication and authorization work, what the platform does (and which behaviours to build around), the order of work, and the rules. The XRDs in `overlays/control/controllers/crossplane/` of Hangar are the source of truth for field names and limits; if a page contradicts the cluster, the cluster is right.

## Workflow

- One issue per change, a short branch cut from the current `origin/main`, one pull request with `Closes #N`. Never commit or push to `main`. Text is in English.
- `main` is protected: the checks `lint`, `test`, `go-mod-tidy`, `govulncheck`, `yamllint`, `hadolint`, `image` and `governance / pr-governance` must pass.
- Releases are cut by release-please from Conventional Commits (`fix:` patch, `feat:` minor, `chore:`/`docs:`/`test:`/`ci:` nothing). Merging the open "chore(main): release x.y.z" pull request is the release: it tags and builds `ghcr.io/phorge-fr/phorge-api:x.y.z`. Hangar pins the image as `<tag>@<digest>` and Renovate opens the bump there. The release workflow uses a PAT secret, because pull requests and tags created with `GITHUB_TOKEN` start no workflow.
- A stuck CI job with zero steps: force-cancel it (`gh api -X POST repos/<repo>/actions/runs/<id>/force-cancel`), then rerun.

## Commands

```bash
make vet test build        # what CI runs
docker build -t phorge-api .
```

The Go version is the one in `go.mod`.

## Rules specific to this repository

- Never print, log or commit a secret, a key or a token. Configuration comes from the environment.
- **Do not change the platform by hand.** An XRD, a composition, the OpenFGA model, RBAC, a firewall rule or an Authentik blueprint is a pull request on Hangar (or Network), never an edit on the cluster: Flux reverts live edits. Use a `git worktree` for the Hangar change if its checkout holds someone else's branch.
- Test tenants are named `apitest-<run>-*`. Never touch `e2e-*` tenants (the platform suite's), the Incus projects `ct-poc` and `default`, or a tenant you did not create. Clean up what you create: children first, then the tenant, then wait until the Incus project is gone.
- API keys are personal: `phk_<id>_<secret>`, owned by a user, acting as that user within scopes, expiring within a year; only a person with an Authentik token manages keys, and a key cannot create keys. See Hangar `docs/decisions/0007-tenant-policy.md`. This repository's own pages are in [docs/](docs/README.md).
- When something is missing or ambiguous (public host names, who may create tenants), ask; do not invent.
