# neptide

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Serraniel/neptide/badge)](https://scorecard.dev/viewer/?uri=github.com/Serraniel/neptide)

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
| `neptide.window` | e.g. `Sat,Sun 03:00-05:00`, `daily 02:00-04:00; Wed 12:00-13:00` | `--window` (any time): apply updates only then, report them as available otherwise |
| `neptide.hook.pre-update` / `neptide.hook.post-update` | a command, e.g. `pg_dump -U app app > /backup/app.sql` | none; see [Hooks](#hooks) |
| `neptide.hook.timeout` | duration | `5m` per hook |
| `neptide.restart` | interval (`6h`) or window (`daily 04:00-05:00`) | none: restart on a schedule, behind the health gate |
| `neptide.snapshot` | `true` / `false` | `--snapshot` (off): copy volumes and bind mounts before an update, restore them on rollback |
| `neptide.snapshot.binds` | `true` / `false` | `false`: only named volumes are snapshotted |
| `neptide.snapshot.exclude` | mount paths, e.g. `/media,/cache` | none |

Never updated: swarm tasks (their service is), neptide itself, `docker compose run` containers (they carry their service's labels but are one-off commands) and containers started with `--rm`, because the daemon deletes the old container on stop and a failed update could not be rolled back.

On Swarm, put labels under `deploy.labels`, the service labels. Images pinned to a digest (`image@sha256:…`) and locally built images are never touched.

## Options

Every flag can also be set as an environment variable, `NEPTIDE_<FLAG>`; for example `--monitor-only` becomes `NEPTIDE_MONITOR_ONLY=true`.

`--interval 6h` · `--window` · `--once` · `--monitor-only` · `--all` · `--track` · `--health-timeout` · `--stable 15s` · `--swarm-timeout 15m` · `--containers` / `--services` · `--notify <urls>` · `--notify-digest` · `--notify-on available,beyond_track,updated,rolled_back,failed` · `--docker-config` · `--insecure-registries` · `--metrics-listen` · `--kuma-url` / `--kuma-username` / `--kuma-password-file` · `--log-json`

### Metrics

`--metrics-listen :9090` serves Prometheus metrics at `/metrics` (and `/healthz`); it is off by default. Metric names start with `neptide_`:
- `events_total{kind}` counts events by kind;
- `cycles_total{result}` counts finished cycles (`ok` or `error`);
- `last_cycle_timestamp_seconds`, `last_success_timestamp_seconds` and `last_cycle_duration_seconds` describe the most recent cycle;
- `updates_pending`, plus `update_pending{kind,name,reason}` per container or service, show updates found but not applied (`reason` is `available` in notify mode, `deferred` while waiting for a maintenance window);
- `build_info{version}` is always 1.

A cycle counts as a success only if every target was checked and handled; a failed or rolled-back target makes it `partial`. To notice a stuck or failing updater, alert on `time() - neptide_last_success_timestamp_seconds > 2 * <interval> + <longest cycle>` with a `for:` of a few minutes; a cycle with health gates or swarm updates can take a while, and the value is 0 until the first success. `increase(neptide_events_total{kind=~"failed|rolled_back"}[1h]) > 0` reports individual failures right away. The endpoint has no authentication and reveals container names, so keep it on an internal network.

### Hooks

`neptide.hook.pre-update` runs inside the old container right before an update (after the new image is pulled, before anything is stopped), for example a database dump. If it fails or times out, the update is cancelled and the container keeps running untouched. `neptide.hook.post-update` runs inside the new container once it is healthy, for example a smoke test or a cache warm-up; if it fails, the update is rolled back like a failed health check.

Hooks run with `sh -c`. Images without a shell take a JSON argv, as in a Dockerfile: `neptide.hook.pre-update=["/app/backup", "--now"]`. Each hook may take `neptide.hook.timeout` (default 5m). Docker cannot stop a command started with exec, so neptide wraps shell hooks in `timeout -s KILL` where the image has a working one. GNU coreutils kills the whole hook; busybox (Alpine) only kills the shell, so children of a compound hook (`a && b`) and JSON-form hooks may keep running after neptide gives up. A failing hook's last output (up to 4 KiB) goes into the event and its notifications, so keep secrets out of what it prints. A container that is crash-looping or paused is updated without its pre-update hook. Hooks apply to containers; swarm services are updated by Swarm itself.

### Scheduled restarts

Some containers degrade over days, and the usual workaround is a sidecar that recreates them on a timer. `neptide.restart` does it with the same safety as an update: an interval (`6h`: when the container has been running that long) or a window in the maintenance-window syntax (`daily 04:00-05:00`: once per window), then the health gate, and containers sharing its network follow it. A restart that fails its health check is reported as `failed`; a successful one as `restarted` (add it to `--notify-on` to be told). The window needs an `--interval` shorter than itself, as for updates. `--monitor-only` only logs which restarts are due. Restarts follow their own schedule: `neptide.window` and `neptide.mode` apply to updates only. Paused containers are left alone, and swarm services ignore the label (Swarm restarts tasks itself). A memory limit (`mem_limit`) is often the better fix for a slow leak.

### Notifications

`--notify` takes [shoutrrr](https://shoutrrr.nickfedor.com/) URLs (Discord, Slack, Teams, Telegram, ntfy, Gotify, e-mail, generic webhooks …), and `--notify-on` picks the event kinds. Every message has a title such as `neptide@nas: updated web` and a level (`info`, `warning` for releases outside the track, `error` for failures and rollbacks), which services that support them show as an embed title and colour or a priority. Set `NEPTIDE_HOST_NAME` when the container's hostname is not meaningful.

`--notify-digest` sends one message per cycle instead of one per event: the title counts the events (`neptide@nas: 1 failed, 3 updated`), the body lists them (cut at about 3500 characters with "… and N more", so it fits every service), and the level is the worst one.

A URL that sets its own `title` or `subject` (e.g. `ntfy://…?title=Updates`, `smtp://…?subject=…`) keeps it, and neptide's title goes into the first line of the message instead. `NEPTIDE_HOST_NAME` is an environment variable only.

### Maintenance windows

`neptide.window` (or `--window` for every target) names when updates may be applied: entries of `DAYS HH:MM-HH:MM` separated by `;`, where DAYS is `daily`, a day (`Mon`), a range (`Mon-Fri`) or a list (`Sat,Sun`). A range may end past midnight (`Fri 22:00-02:00` belongs to Friday). Times follow the container's `TZ`, e.g. `TZ=Europe/Berlin`.

Outside the window, an update is reported as a `deferred` event (not notified by default; add it to `--notify-on` if you want to hear about it) and applied when the window opens: neptide shortens its next sleep to wake up right then, so a window shorter than `--interval` is never missed, and `Sun 03:00-04:00` updates start at Sunday 03:00. An empty `neptide.window=""` label lifts a default window for one container.

`TZ` must be a zone name such as `Europe/Berlin`; neptide refuses to start with an unknown one instead of silently using UTC, and logs the zone it uses. Windows follow the wall clock, so on daylight-saving days a window between 02:00 and 03:00 is skipped (spring) or open twice as long (autumn); avoid that hour. Containers sharing the network of an updated container (`network_mode: container:X`) are restarted with it, whatever their own window says, because they would lose their network otherwise.

### Uptime Kuma maintenance

With `--kuma-url https://kuma.example.org --kuma-username neptide` and the password in `--kuma-password-file` (or `NEPTIDE_KUMA_PASSWORD`, never a flag), a container's monitors go into a manual maintenance window while it is recreated. The window ends once the health gate passes or the update is rolled back.

- **Which monitors:** they're found through the container's AutoKuma labels (`kuma.<id>.<type>.name`), by AutoKuma tag or by a name exactly one monitor has. Ambiguous names are skipped, so another host's monitor is never silenced.
- **Opting out:** `neptide.maintenance=false` (or NepFleet's `nepfleet.maintenance=false`) keeps a container's monitors out.
- **If neptide dies:** the window is scheduled to end by itself after the health timeout plus 45 minutes (it also covers the pull and a snapshot), so it never stays open.
- **If Kuma is down:** the update still runs, and a warning is logged. An unreachable Kuma can add up to 30 seconds per updated container for the login, and the same again for closing the window.

The client is `pkg/kuma`, shared with NepFleet.

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

**Containers sharing its network** (`network_mode: container:X` or `service:X`, as behind a VPN container) lose their network when X restarts, and cannot start at all once X is replaced. After X is updated or rolled back, neptide restarts them or recreates them unchanged in the new X's namespace, then waits for their health gate. They do not need `neptide.enable`, and their image is not updated by this.

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
