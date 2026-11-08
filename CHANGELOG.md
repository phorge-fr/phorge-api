# Changelog

## [0.7.0](https://github.com/phorge-fr/phorge-api/compare/v0.6.0...v0.7.0) (2026-11-08)


### ⚠ BREAKING CHANGES

* **profiles:** MAX_PROFILES_PER_TENANT is required, like the other values that decide how the API behaves.
* **tenants:** TENANT_CREATION and MAX_TENANTS_PER_USER are required, like the other values that decide how the API behaves, and deleting a tenant is no longer for the platform administrators only: its owner may.
* **quota:** RATE_LIMIT_WRITES_PER_MINUTE, RATE_LIMIT_BURST and MAX_IMAGES_PER_TENANT are required, like the other values that decide how the API behaves: the deployment must write them before it runs this version.

### Features

* **apikeys:** let a key hold tenants:write, networks:read and networks:write ([694c68b](https://github.com/phorge-fr/phorge-api/commit/694c68bd361c4cb09460ba0ee1b8652408c8f63e))
* **apikeys:** let a key hold tenants:write, networks:read and networks:write ([da2b9ff](https://github.com/phorge-fr/phorge-api/commit/da2b9ff13f0d0a5edbe4e8aa923faacd847ca040))
* **networks:** let users deploy networks and attach their instances to them ([5a1e307](https://github.com/phorge-fr/phorge-api/commit/5a1e307a6581b2eb6a34b66730ae8922d5c869f3))
* **networks:** let users deploy networks and attach their instances to them ([7daa9d9](https://github.com/phorge-fr/phorge-api/commit/7daa9d9ccc73a9fdfc8580151e2aeba8ab7e1be6))
* **profiles:** let users make and delete their own profiles ([fd05be8](https://github.com/phorge-fr/phorge-api/commit/fd05be8e522136f3a1a3267ea7b6a9cb4412d14f))
* **profiles:** let users make and delete their own profiles ([b0a1d63](https://github.com/phorge-fr/phorge-api/commit/b0a1d639f84c19c32ed5e1e42b8dabf4bfe84d78))
* **profiles:** size an instance with a profile of its tenant ([ea7de4a](https://github.com/phorge-fr/phorge-api/commit/ea7de4a831c88f0a9e99bed84909cad9e053d1bf))
* **profiles:** size an instance with a profile of its tenant ([f6e21f1](https://github.com/phorge-fr/phorge-api/commit/f6e21f191eb4966a653a27249eb863cd009146c3))
* **quota:** align the plans with the profiles: 2, 4 and 6 instances ([b2fe4e6](https://github.com/phorge-fr/phorge-api/commit/b2fe4e630e01f2969b062a99ba3fd580d306c954))
* **quota:** allow 2, 4 and 6 instances in the small, medium and large plans ([ced9a7c](https://github.com/phorge-fr/phorge-api/commit/ced9a7cc91b7f27106b2a0ccd26c6dafda7e7cf4))
* **quota:** hold two instances of the size of each profile in every plan ([6234f01](https://github.com/phorge-fr/phorge-api/commit/6234f01c7c991f70a07bb310d687bbb00f524d1c))
* **quota:** say which limit a request passes, and slow down who writes too much ([8be2f13](https://github.com/phorge-fr/phorge-api/commit/8be2f13a919cf87d7c799938cb8ca6be319f58b9))
* **quota:** say which limit a request passes, and slow down who writes too much ([803c5f9](https://github.com/phorge-fr/phorge-api/commit/803c5f9784961b9d2d13429b68f72b4c7e2f7fed))
* **status:** tell a network limit in words ([c675256](https://github.com/phorge-fr/phorge-api/commit/c675256391618fd9d452c5f4b99ffdbdfa007f0e))
* **status:** tell the user what is wrong in words, not Crossplane conditions ([6d9e1b3](https://github.com/phorge-fr/phorge-api/commit/6d9e1b37d05b002e17f338456c61904c4a75f65e))
* **status:** tell the user what is wrong in words, not Crossplane conditions ([74637ae](https://github.com/phorge-fr/phorge-api/commit/74637aeab4588c08a71ade72879a781902d573ad))
* **tenants:** let a person create, rename and delete their own tenant ([de164e2](https://github.com/phorge-fr/phorge-api/commit/de164e2c31295e7ff2aa15187a1a02b1f4158b6e))
* **tenants:** let a person create, rename and delete their own tenant ([2323f89](https://github.com/phorge-fr/phorge-api/commit/2323f89a350b6de469c979b71dac87167dad8dfc))


### Bug Fixes

* **deps:** update module github.com/getkin/kin-openapi to v0.153.0 ([289894f](https://github.com/phorge-fr/phorge-api/commit/289894f2037bf91668f71956f9bee34fae924810))
* **deps:** update module github.com/getkin/kin-openapi to v0.153.0 ([3df3a1e](https://github.com/phorge-fr/phorge-api/commit/3df3a1ecd831b56fb9ab1122b9c195233f583f21))
* **tenants:** delete what a tenant holds before the tenant ([357d171](https://github.com/phorge-fr/phorge-api/commit/357d17131638e2b397d841ada2bbc96299866ada))
* **tenants:** delete what a tenant holds before the tenant ([13334be](https://github.com/phorge-fr/phorge-api/commit/13334be20d2c9542fa0b5f2ca8c722fc9cb26cef))

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
