# neptide

Keeps Docker containers and **Swarm services** on their newest image, and rolls them back when the new image is unhealthy.

It works standalone as a replacement for Watchtower or Shepherd, and it is the update engine of [NepFleet](https://github.com/Serraniel/nepfleet).

- **Health-gated.** A container is replaced only once its successor passes its `HEALTHCHECK`, or keeps running for a while if it has none. Otherwise the old container is restored, with the same name and config.
- **Swarm-native.** Services are updated through Swarm's own rolling update, pinned to the new digest. Services without an `update_config` get a safe one: one task at a time, roll back on failure.
- **Tracks, not surprises.** Each image follows its tag's digest (`digest`, the default) or moves within `patch`, `minor` or `major`. Releases outside the track are reported, never applied.
- **Notifications** go to Discord, Slack, Telegram, Matrix, ntfy, Gotify, e-mail and webhooks, through [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) URLs.
- **Small and unprivileged.** A static binary on `scratch`, running as uid 65532 and talking to the Docker API directly.

## Run

Standalone host: see [compose.yaml](compose.yaml).

```sh
DOCKER_GID=$(getent group docker | cut -d: -f3) docker compose up -d
```

Swarm: see [stack.yaml](stack.yaml). It runs one service updater on a manager, plus an optional global updater for standalone containers.

## Opt in with labels

| Label | Values | Default |
|---|---|---|
| `neptide.enable` | `true` / `false` | off; `--all` turns it on for everything |
| `neptide.track` | `digest`, `patch`, `minor`, `major` | `--track` (`digest`) |
| `neptide.mode` | `update`, `notify` | `update`; `--monitor-only` wins |
| `neptide.health-timeout` | duration, e.g. `3m` | `--health-timeout` (`2m`) |
| `neptide.snapshot` | `true` / `false` | `--snapshot` (off): copy volumes and bind mounts before an update, restore them on rollback |
| `neptide.snapshot.binds` | `true` / `false` | `false`: only named volumes are snapshotted |
| `neptide.snapshot.exclude` | mount paths, e.g. `/media,/cache` | none |

On Swarm, put labels under `deploy.labels`, the service labels. Images pinned to a digest (`image@sha256:…`) and locally built images are never touched.

## Options

Every flag can also be set as an environment variable, `NEPTIDE_<FLAG>`; for example `--monitor-only` becomes `NEPTIDE_MONITOR_ONLY=true`.

`--interval 6h` · `--once` · `--monitor-only` · `--all` · `--track` · `--health-timeout` · `--stable 15s` · `--swarm-timeout 15m` · `--containers` / `--services` · `--notify <urls>` · `--notify-on available,beyond_track,updated,rolled_back,failed` · `--docker-config` · `--insecure-registries` · `--log-json`

### Private registries

neptide reads credentials from a Docker `config.json` (`--docker-config`, default `$DOCKER_CONFIG/config.json` or `~/.docker/config.json`): a `credHelpers` entry for the registry first, then `credsStore`, as Docker picks them. One deliberate difference: when the helper is missing, fails or knows nothing about the registry, neptide still tries the inline `auths`, where Docker would not. So a config copied from a desktop (`credsStore: desktop`) can carry inline tokens for neptide.

Credential helpers are programs named `docker-credential-<name>` on `PATH`; neptide runs them as `docker-credential-<name> get`. Answers are reused for 5 minutes, so short-lived tokens (ECR, GCR) stay fresh. A helper that is not installed is logged once; one that fails or takes longer than 30 seconds is logged, and neptide falls back to inline auths, then anonymous access. Mount helpers into `/usr/local/bin`, the image's `PATH`. Identity tokens (`"Username": "<token>"`, used by some OAuth logins) are not supported yet.

The image is `FROM scratch`, so a helper must be a static binary you mount into it, such as `docker-credential-ecr-login` or `docker-credential-gcr`:

```yaml
    volumes:
      - ./config.json:/config/config.json:ro
      - /usr/local/bin/docker-credential-ecr-login:/usr/local/bin/docker-credential-ecr-login:ro
    environment:
      NEPTIDE_DOCKER_CONFIG_FILE: /config/config.json
```

Desktop keychains (`pass`, `secretservice`, `osxkeychain`) need a user session and do not work in the container. For those, run neptide natively or put inline credentials (a read-only token) in the config.json you mount.

## How an update runs

**Container:**
1. Pull the new image.
2. Rename the old container to `<name>-neptide-old` and stop it.
3. Create the new container with the same config. Settings that were only the old image's defaults (CMD, ENV, healthcheck and so on) are dropped, so the new image's defaults apply.
4. Start it and wait for the health gate.
5. On success, remove the old container. On failure, remove the new one, then rename and start the old one.

**With `neptide.snapshot=true`,** step 2 also copies the stopped container's writable volumes and bind mounts into snapshot volumes (`neptide-snap-*`, the last 3 kept). A rollback restores them before the old container starts, so it never runs on data the new version has already migrated. If the restore fails, the old container stays stopped instead of starting on that data.

By default only **named local volumes** are snapshotted. Bind mounts need `neptide.snapshot.binds=true` on the container. Never snapshotted, even then:
- read-only mounts;
- system trees (`/etc`, `/usr`, `/proc`, `/tmp`, `/var/log`, Docker's root, …) and shared roots such as `/`, `/home`, `/srv` themselves;
- bind sources that are symlinks elsewhere or contain other mounts (a restore must never detach an NFS share);
- volumes backed by host paths or remote storage;
- data another running container mounts.

Skipped mounts are logged. A snapshot that would leave less than 10 % of the disk free is refused, and the update with it. Copies keep ownership, xattrs, ACLs, hardlinks and sparse files.

**Service:**
1. Change the image to `name:tag@digest`, with `update_config` defaulted as described above.
2. Wait for Swarm to report `completed` or `rollback_completed`.

## Develop

```sh
go test ./...
CGO_ENABLED=0 go build -o /tmp/neptide ./cmd/neptide
test/integration/compose.sh /tmp/neptide   # uses your Docker daemon
test/integration/swarm.sh /tmp/neptide     # isolated in docker:dind
```

Releases, SBOMs and signatures follow [NepFleet's process](https://github.com/Serraniel/nepfleet/blob/main/docs/releases.md). License: [AGPL-3.0-or-later](LICENSE).
