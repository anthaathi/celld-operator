# Changelog

## [1.0.1](https://github.com/anthaathi/celld-operator/compare/v1.0.0...v1.0.1) (2026-09-01)


### Bug Fixes

* **ci:** attach release assets by explicit release ID via gh api ([e95a403](https://github.com/anthaathi/celld-operator/commit/e95a403a7331f984ffeebd0eda79957f1466798c))
* **ci:** build release assets from release-please via reusable workflow ([90b638e](https://github.com/anthaathi/celld-operator/commit/90b638e327d01cbf028329de5c82fb4161e65745))
* **ci:** upload release assets to the release upload_url endpoint ([a4aa824](https://github.com/anthaathi/celld-operator/commit/a4aa824592503a919a347c365af809bac5ef006a))

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
