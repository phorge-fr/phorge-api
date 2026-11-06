# Changelog

## [0.6.0](https://github.com/phorge-fr/phorge-api/compare/v0.5.1...v0.6.0) (2026-11-06)


### Features

* **auth:** accept the access tokens of more than one OIDC client ([88d11a3](https://github.com/phorge-fr/phorge-api/commit/88d11a35c3fb2d64c9c52a1c7310f5b4e20dde15))
* **auth:** accept the access tokens of more than one OIDC client ([5075899](https://github.com/phorge-fr/phorge-api/commit/5075899fafa64b606e65ce7266d8c95d9ebd87e4))

## [0.5.1](https://github.com/phorge-fr/phorge-api/compare/v0.5.0...v0.5.1) (2026-11-06)


### Bug Fixes

* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.8 ([1e2b8a8](https://github.com/phorge-fr/phorge-api/commit/1e2b8a8ad07ed486a6413817974fd474dda402d3))
* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.8 ([8cf01cb](https://github.com/phorge-fr/phorge-api/commit/8cf01cbdcb39b83baff2a55da7c249a78bb39ecb))
* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.9 ([a893883](https://github.com/phorge-fr/phorge-api/commit/a8938837ef90fd5b4b3a342f46b2a86674d5dc75))
* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.9 ([578feba](https://github.com/phorge-fr/phorge-api/commit/578feba62ce1646eaae9015e3a21cf3fb25bc679))

## [0.5.0](https://github.com/phorge-fr/phorge-api/compare/v0.4.2...v0.5.0) (2026-11-02)


### Features

* **apikeys:** create keys as Authentik service accounts ([b61eeb2](https://github.com/phorge-fr/phorge-api/commit/b61eeb207e23a6913fde0a0028365a9a11ad8e25))
* **apikeys:** create keys as Authentik service accounts ([1a1f0ed](https://github.com/phorge-fr/phorge-api/commit/1a1f0edee31f6d0a6236a1f34d94fea4b1f60e99))
* **auth:** accept service accounts of Authentik as API keys ([fded19f](https://github.com/phorge-fr/phorge-api/commit/fded19f5dfa8046a17f66490f3ce2e2ca3eb17ab))
* **fga:** send the current time with every Check so key tuples can expire ([74c7cab](https://github.com/phorge-fr/phorge-api/commit/74c7cab348dec30dde2cc1af05eec7dc8e78a5b4))

## [0.4.2](https://github.com/phorge-fr/phorge-api/compare/v0.4.1...v0.4.2) (2026-10-31)


### Bug Fixes

* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.7 ([f09cac2](https://github.com/phorge-fr/phorge-api/commit/f09cac21bffeebc08db415810f6b34766abb0526))
* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.7 ([3621651](https://github.com/phorge-fr/phorge-api/commit/36216513a01e9a0d10b765a41e9b7e86df414603))

## [0.4.1](https://github.com/phorge-fr/phorge-api/compare/v0.4.0...v0.4.1) (2026-10-29)


### Bug Fixes

* **deps:** update kubernetes monorepo to v0.37.2 ([8248d6c](https://github.com/phorge-fr/phorge-api/commit/8248d6c2caabce2625e58d870a3cd21e54d597a4))
* **deps:** update kubernetes monorepo to v0.37.2 ([a7a7b0e](https://github.com/phorge-fr/phorge-api/commit/a7a7b0e28eef38ee6e835a2a4a1b0d953ebdc09d))

## [0.4.0](https://github.com/phorge-fr/phorge-api/compare/v0.3.0...v0.4.0) (2026-10-27)


### ⚠ BREAKING CHANGES

* **config:** no value that decides the behaviour is written in the code

### Bug Fixes

* **config:** no value that decides the behaviour is written in the code ([6fdabc4](https://github.com/phorge-fr/phorge-api/commit/6fdabc40bae5c2dea24325e5e375eb65353ad8b0))

## [0.3.0](https://github.com/phorge-fr/phorge-api/compare/v0.2.0...v0.3.0) (2026-10-27)


### Features

* **instances:** say what a VM is doing while it changes state ([76495c5](https://github.com/phorge-fr/phorge-api/commit/76495c5f571a8bff8c6938d333c3dab144753d11))
* **instances:** say what a VM is doing while it changes state ([15bc7f6](https://github.com/phorge-fr/phorge-api/commit/15bc7f6d1d1d9e909ec70c56f7dca22b4cd1cf8f))


### Bug Fixes

* **instances:** date a transition from the request, not from Crossplane ([b5c68f1](https://github.com/phorge-fr/phorge-api/commit/b5c68f1d22c5f27deae8c7fc36b6347b2360412f))
* **instances:** date a transition from the request, not from Crossplane ([e77b493](https://github.com/phorge-fr/phorge-api/commit/e77b4934de1f190e874b2b71b9c90381e256160e))
* **instances:** refuse to stop a VM that is still booting ([832989b](https://github.com/phorge-fr/phorge-api/commit/832989b0ff04ecb0c5bc4313f5af6f5174f8e404))
* **instances:** refuse to stop a VM that is still booting ([4f6e563](https://github.com/phorge-fr/phorge-api/commit/4f6e56331b4a4cb5a9d55013cd28f80d6c258624))

## [0.2.0](https://github.com/phorge-fr/phorge-api/compare/v0.1.1...v0.2.0) (2026-10-26)


### ⚠ BREAKING CHANGES

* personal API keys that act as their owner

### Features

* personal API keys that act as their owner ([5a6584b](https://github.com/phorge-fr/phorge-api/commit/5a6584b7b17dfd569f6b9dfb0982e60a5052d47d))

## [0.1.1](https://github.com/phorge-fr/phorge-api/compare/v0.1.0...v0.1.1) (2026-10-26)


### Bug Fixes

* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.6 ([5c87119](https://github.com/phorge-fr/phorge-api/commit/5c8711982234c9f9aaef5d41a19e145032bc26e3))
* **deps:** update module github.com/go-jose/go-jose/v4 to v4.1.6 ([1bc0e6b](https://github.com/phorge-fr/phorge-api/commit/1bc0e6b5ac075e5a9f799b7b96a91d8b09035521))
