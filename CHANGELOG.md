# Changelog

## 1.0.0 (2026-09-01)


### Features

* ingress exposure, CelldObjectStore, kubectl-celld plugin, GitHub Actions ([f88669e](https://github.com/anthaathi/celld-operator/commit/f88669e26bc39801eb13d882ccc6137ae1264c7d))
* release automation with release-please, E2E suite, and CLI tests ([62fe67a](https://github.com/anthaathi/celld-operator/commit/62fe67aa03e6b6bb2a1209f527c0967581894f78))


### Bug Fixes

* bump VERSION as a plain version file, not annotated generic ([1e89603](https://github.com/anthaathi/celld-operator/commit/1e8960385e69efd0bf0ef9e831e67a8eaffbf7b3))
* correct release-please manifest format (path to version string) ([6b8eab9](https://github.com/anthaathi/celld-operator/commit/6b8eab94d3ae5c35a367ad811582b35d762b2a4b))
* **e2e:** pin deployer image to the locally loaded kind image ([898bcda](https://github.com/anthaathi/celld-operator/commit/898bcda0707e1f5b824626528060044dd5dddbbf))
* **e2e:** probe worker over port-forward instead of in-container curl ([ec1734f](https://github.com/anthaathi/celld-operator/commit/ec1734f144ffd4a2e4a60b84d71bdd8ee19ee34a))
* **rbac:** add lease permissions for leader election ([3acca08](https://github.com/anthaathi/celld-operator/commit/3acca08c49655ba766cc5ad270576d20e15e3d08))
