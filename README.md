# phorge-api

The Phorge API: authenticated users and API keys manage tenants, images, instances, memberships and API keys.
It creates `phorge.fr` objects on the `control` cluster; Crossplane turns them into Incus and OpenFGA objects.

## Build and run

```bash
make vet lint test build
OIDC_ISSUER=... OIDC_AUDIENCE=... OPENFGA_URL=... OPENFGA_API_KEY=... OPENFGA_STORE_ID=... ./bin/phorge-api
```

The configuration is read from the environment; the process refuses to start and lists everything wrong if a required value is missing. See [Configuration](docs/reference/configuration.md).

## Documentation

Pages are in [docs/](docs/README.md), grouped by what you want to do: how-to guides, reference, explanation.

The kinds the API manages and the platform behind them are documented in `phorge-fr/Hangar`: [Phorge API kinds](https://github.com/phorge-fr/Hangar/blob/main/docs/reference/phorge-api-kinds.md).

## Contributing

Every change starts from an issue and goes through a pull request; the rules (issue link, label, milestone, assignee, reviewer) are in the organization's [GOVERNANCE](https://github.com/phorge-fr/.github/blob/main/GOVERNANCE.md) and [CONTRIBUTING](https://github.com/phorge-fr/.github/blob/main/CONTRIBUTING.md). Planned work is on the public [Roadmap](https://github.com/orgs/phorge-fr/projects/3). Everyone taking part follows the [Code of Conduct](https://github.com/phorge-fr/.github/blob/main/CODE_OF_CONDUCT.md).

## Security

Report vulnerabilities privately, as described in the [security policy](https://github.com/phorge-fr/.github/blob/main/SECURITY.md). Do not open a public issue for them.

## License

Licensed under the [Apache License 2.0](LICENSE).
