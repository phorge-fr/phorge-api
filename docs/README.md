# phorge-api documentation

Pages are grouped by what the reader wants, following [the organization's documentation standard](https://github.com/phorge-fr/.github/blob/main/docs/reference/documentation-standard.md). The kinds the API manages are documented in Hangar: [Phorge API kinds](https://github.com/phorge-fr/Hangar/blob/main/docs/reference/phorge-api-kinds.md).

## How-to guides

| Page | Task |
| --- | --- |
| [Get a token with the device flow](how-to/get-a-token-with-the-device-flow.md) | Call the API from a terminal with your identity |
| [Run the integration tests](how-to/run-the-integration-tests.md) | Test against the real cluster and OpenFGA store |
| [Release the API](how-to/release-the-api.md) | Cut a version with release-please |

## Reference

| Page | Content |
| --- | --- |
| [Configuration](reference/configuration.md) | Environment variables |
| [Endpoints](reference/endpoints.md) | Routes |
| [Authentication](reference/authentication.md) | Accepted credentials |
| [Tenants](reference/tenants.md) | Behavior of the tenant routes |
| [Images and instances](reference/images-and-instances.md) | Behavior of the image and instance routes |
| [Memberships and API keys](reference/memberships-and-api-keys.md) | Roles and personal keys |

## Explanation

| Page | Question it answers |
| --- | --- |
| [How the API authorizes a request](explanation/authorization.md) | How OpenFGA and key scopes decide |
