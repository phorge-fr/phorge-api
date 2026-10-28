---
type: how-to
audience: maintainer who releases a new version of the API
verified: 2026-10-28
---

# Release the API

Releases are cut by [release-please](https://github.com/googleapis/release-please) from the commit messages (Conventional Commits). On every push to `main` it keeps one pull request open, "chore(main): release x.y.z", that bumps `.release-please-manifest.json` and writes `CHANGELOG.md`. Merging that pull request is the release: it creates the tag and the GitHub release, and the tag builds and pushes the image `ghcr.io/phorge-fr/phorge-api:x.y.z`. Nothing is released until you merge it.

- `fix:` gives a patch, `feat:` a minor (while the version is below 1.0.0, a breaking change `feat!:` also gives a minor), `chore:`, `docs:`, `test:` and `ci:` give nothing.
- Hangar pins the image as `<tag>@<digest>`: Renovate opens the pull request there when a release is published.
- The workflow uses the `RENOVATE_TOKEN` secret, because pull requests and tags created with `GITHUB_TOKEN` start no workflow, so the checks of the release pull request and the image build would never run.
