# neptide as a library

neptide's own updater lives in `internal/`. The packages under `pkg/` are public: [NepFleet](https://github.com/Serraniel/nepfleet) imports them, and other Go programs may too.

| Package | What it does |
|---|---|
| `pkg/engine` | A minimal Docker Engine API client: just what updating needs (containers, inspect). |
| `pkg/kuma` | Uptime Kuma's Socket.IO API: login, monitors, maintenance windows and incidents. |
| `pkg/registry` | A small read-only OCI distribution client: tags, digests, manifests. |
| `pkg/rollout` | Updates Compose containers and Swarm services to a new image, health-gated, with rollback. |
| `pkg/sigcheck` | Tells whether an image digest carries a valid Sigstore (cosign keyless) signature by an expected identity. |
| `pkg/snapshot` | Copies a stopped container's writable data (volumes and binds) before an update, and restores it. |
| `pkg/version` | Compares image tags that carry version numbers, by track (major, minor, patch). |

## Compatibility

- **Within a minor version** (0.2.x), the exported API of `pkg/` stays compatible: existing code keeps compiling and means the same.
- **Checked on every pull request:** CI compares `pkg/` with the last release using [apidiff](https://pkg.go.dev/golang.org/x/exp/cmd/apidiff). An incompatible change fails, unless the PR title marks it breaking (`feat!: …`). Release-please then names it in the changelog and makes the next release a new minor version while neptide is below 1.0.
- **Additions** (new functions, types, fields) are compatible, and come in minor releases with `feat`.
- **`internal/`** may change at any time.
