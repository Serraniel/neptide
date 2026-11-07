# Changelog

## 0.1.0 (2026-11-07)


### Features

* default status page, and maintenance windows that show on status pages ([#37](https://github.com/Serraniel/neptide/issues/37)) ([bdd451a](https://github.com/Serraniel/neptide/commit/bdd451ab8c9211a8be919dfc4b1fa9259d577097))
* Docker credential helpers for private registries ([#22](https://github.com/Serraniel/neptide/issues/22)) ([7200757](https://github.com/Serraniel/neptide/commit/7200757f65c4b1f99f5b1e6c29e8ad7cd38b77cd))
* initial version ([3611f2d](https://github.com/Serraniel/neptide/commit/3611f2d28b752329e73c17b0aa79e2f1035644ae))
* **kuma:** status-page incidents for degraded, not down, services ([#35](https://github.com/Serraniel/neptide/issues/35)) ([e6ced92](https://github.com/Serraniel/neptide/commit/e6ced9233bde11cc424593fc34d5211da0d525bd))
* maintenance windows for applying updates (neptide.window, --window) ([#24](https://github.com/Serraniel/neptide/issues/24)) ([c088a28](https://github.com/Serraniel/neptide/commit/c088a28cd08c6d55ddaebf8758a61ad9ff024b33))
* pre- and post-update hooks inside the container ([#27](https://github.com/Serraniel/neptide/issues/27)) ([1bf4b97](https://github.com/Serraniel/neptide/commit/1bf4b97d2bb30c03ab30aca94fe5dd39ad298313))
* Prometheus metrics endpoint (--metrics-listen) ([#23](https://github.com/Serraniel/neptide/issues/23)) ([68ffa8d](https://github.com/Serraniel/neptide/commit/68ffa8da6861e3b97ff0e02b66927e9e55458cfe))
* scheduled restarts behind the health gate (neptide.restart) ([#30](https://github.com/Serraniel/neptide/issues/30)) ([c8e0077](https://github.com/Serraniel/neptide/commit/c8e0077582c1eb8ca0bd53b5e29b62a9f3e13899))
* snapshot volumes before an update, restore them on rollback ([#20](https://github.com/Serraniel/neptide/issues/20)) ([a7a7875](https://github.com/Serraniel/neptide/commit/a7a7875645490c1f28d71bd394b8363950618eb4))
* titled, levelled notifications and a per-cycle digest ([#28](https://github.com/Serraniel/neptide/issues/28)) ([6017dee](https://github.com/Serraniel/neptide/commit/6017dee323a6ec8370283104351274bca03da4be))
* Uptime Kuma maintenance around container updates ([#31](https://github.com/Serraniel/neptide/issues/31)) ([915e51f](https://github.com/Serraniel/neptide/commit/915e51ff0e97f109cacf5701713a8edbb1dd8adf))


### Bug Fixes

* keep containers that share a network namespace working across updates ([#21](https://github.com/Serraniel/neptide/issues/21)) ([59c429e](https://github.com/Serraniel/neptide/commit/59c429e9eac5ff301f90e4beb6cd911aa3a244ac))
* never update one-off docker compose run containers ([#26](https://github.com/Serraniel/neptide/issues/26)) ([3cf0678](https://github.com/Serraniel/neptide/commit/3cf0678a3d867a83ec18b908b7d8f7a892a947f4))
* update containers whose running image lost its tag ([#16](https://github.com/Serraniel/neptide/issues/16)) ([9662097](https://github.com/Serraniel/neptide/commit/9662097c5112f8d0b8bd3fe8c5240381e333905d))
