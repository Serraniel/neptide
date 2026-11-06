// SPDX-License-Identifier: AGPL-3.0-or-later

// Command neptide keeps Docker containers and swarm services on their newest
// image, with a health gate and automatic rollback.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the image has no zoneinfo; windows follow TZ

	"github.com/Serraniel/neptide/internal/metrics"
	"github.com/Serraniel/neptide/internal/updater"
	"github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/kuma"
	"github.com/Serraniel/neptide/pkg/registry"
	"github.com/Serraniel/neptide/pkg/rollout"
	"github.com/Serraniel/neptide/pkg/version"
)

var (
	buildVersion = "dev"
	commit       = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "neptide:", err)
		os.Exit(1)
	}
}

// env returns NEPTIDE_<NAME> or def, so every flag can be set from compose.
func env(name, def string) string {
	if v, ok := os.LookupEnv("NEPTIDE_" + name); ok {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	v := strings.ToLower(env(name, fmt.Sprint(def)))
	return v == "1" || v == "true" || v == "yes"
}

func envInt(name string, def int) int {
	n, err := strconv.Atoi(env(name, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return n
}

func envDuration(name string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(env(name, def.String()))
	if err != nil {
		return def
	}
	return d
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintf(stdout, "neptide %s (%s)\n", buildVersion, commit)
		return nil
	}
	fs := flag.NewFlagSet("neptide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	interval := fs.Duration("interval", envDuration("INTERVAL", 6*time.Hour), "time between checks")
	once := fs.Bool("once", envBool("ONCE", false), "run one cycle and exit")
	monitor := fs.Bool("monitor-only", envBool("MONITOR_ONLY", false), "report updates, never apply them (overrides neptide.mode)")
	containers := fs.Bool("containers", envBool("CONTAINERS", true), "update standalone and compose containers on this node")
	services := fs.Bool("services", envBool("SERVICES", true), "update swarm services (only acts on a manager; run one such instance per swarm)")
	windowSpec := fs.String("window", env("WINDOW", ""), "default maintenance window for applying updates, e.g. \"Sat,Sun 03:00-05:00\" (local time, TZ); per container: neptide.window")
	snap := fs.Bool("snapshot", envBool("SNAPSHOT", false), "snapshot volumes before every container update and restore them on rollback (per container: neptide.snapshot)")
	snapKeep := fs.Int("snapshot-keep", envInt("SNAPSHOT_KEEP", 3), "data snapshots kept per container")
	all := fs.Bool("all", envBool("ALL", false), "include containers without neptide.enable=true")
	track := fs.String("track", env("TRACK", "digest"), "default track: digest, patch, minor, major")
	healthTimeout := fs.Duration("health-timeout", envDuration("HEALTH_TIMEOUT", 2*time.Minute), "how long a new container may take to become healthy")
	stable := fs.Duration("stable", envDuration("STABLE", 15*time.Second), "how long a container without HEALTHCHECK must keep running")
	swarmTimeout := fs.Duration("swarm-timeout", envDuration("SWARM_TIMEOUT", 15*time.Minute), "upper bound for one swarm service update")
	notify := fs.String("notify", env("NOTIFY", ""), "comma separated shoutrrr URLs (discord://, slack://, ntfy://, generic+https://, ...)")
	notifyDigest := fs.Bool("notify-digest", envBool("NOTIFY_DIGEST", false), "send one notification per cycle that lists all its events, instead of one per event")
	notifyOn := fs.String("notify-on", env("NOTIFY_ON", "available,beyond_track,updated,rolled_back,failed"), "event kinds to notify")
	dockerConfig := fs.String("docker-config", env("DOCKER_CONFIG_FILE", defaultDockerConfig()), "Docker config.json with registry credentials")
	insecure := fs.String("insecure-registries", env("INSECURE_REGISTRIES", ""), "comma separated registry hosts reached over plain HTTP")
	jsonLog := fs.Bool("log-json", envBool("LOG_JSON", false), "log as JSON")
	kumaURL := fs.String("kuma-url", env("KUMA_URL", ""), "Uptime Kuma base URL: monitors from a container's kuma.* labels go into maintenance while it is updated (off when empty)")
	kumaUser := fs.String("kuma-username", env("KUMA_USERNAME", ""), "Uptime Kuma user")
	kumaPassFile := fs.String("kuma-password-file", env("KUMA_PASSWORD_FILE", ""), "file with the Uptime Kuma password (or set NEPTIDE_KUMA_PASSWORD)")
	metricsAddr := fs.String("metrics-listen", env("METRICS_LISTEN", ""), "serve Prometheus metrics on this address, e.g. :9090 (off when empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var handler slog.Handler = slog.NewTextHandler(stderr, nil)
	if *jsonLog {
		handler = slog.NewJSONHandler(stderr, nil)
	}
	log := slog.New(handler)

	kumaTargets, err := kumaTargets(*kumaURL, *kumaUser, *kumaPassFile)
	if err != nil {
		return err
	}

	tr, err := version.ParseTrack(*track)
	if err != nil {
		return err
	}
	window, err := rollout.ParseWindows(*windowSpec)
	if err != nil {
		return fmt.Errorf("--window: %w", err)
	}
	// Go silently falls back to UTC for an unknown TZ, so windows would
	// open at the wrong hour: an error with --window, a warning otherwise
	// (labels may still set windows).
	if err := checkTZ(os.Getenv("TZ")); err != nil {
		if len(window) > 0 {
			return err
		}
		log.Warn(err.Error() + "; maintenance windows in labels would use UTC")
	}
	mode := rollout.ModeUpdate
	if *monitor {
		mode = rollout.ModeNotify
	}

	eng, err := engine.New("")
	if err != nil {
		return err
	}
	reg := registry.New()
	reg.UserAgent = "neptide/" + buildVersion
	for _, h := range split(*insecure) {
		reg.Insecure[h] = true
	}
	creds, err := registry.LoadDockerConfig(*dockerConfig)
	if err != nil {
		return fmt.Errorf("docker config: %w", err)
	}
	creds.Log = func(msg string) { log.Warn(msg) }
	reg.Creds = creds.Lookup

	notifier, err := newNotifier(split(*notify), split(*notifyOn), *notifyDigest, log)
	if err != nil {
		return err
	}

	u := &updater.Updater{
		Engine: eng, Registry: reg, Log: log,
		Opts: updater.Options{
			All:            *all,
			SkipContainers: !*containers,
			SkipServices:   !*services,
			ForceNotify:    *monitor, // wins over neptide.mode=update labels
			Defaults:       rollout.Policy{Track: tr, Mode: mode, HealthTimeout: *healthTimeout, Snapshot: *snap, Window: window},
			SnapshotKeep:   *snapKeep,
			Gate:           rollout.Gate{Timeout: *healthTimeout, Stable: *stable},
			SwarmTimeout:   *swarmTimeout,
			Kuma:           kumaTargets,
		},
	}

	if notifier != nil {
		u.Notify = notifier.Event
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m := metrics.New(buildVersion)
	if *metricsAddr != "" && *once {
		log.Warn("--metrics-listen is ignored with --once")
	} else if *metricsAddr != "" {
		srv := &http.Server{Addr: *metricsAddr, Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
		ln, err := net.Listen("tcp", *metricsAddr)
		if err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics server stopped", "err", err)
			}
		}()
		defer func() { _ = srv.Close() }()
		log.Info("metrics on " + ln.Addr().String() + "/metrics")
	}
	log.Info("neptide started", "version", buildVersion, "interval", interval.String(), "monitor_only", *monitor,
		"time_zone", time.Now().Format("MST -0700"))
	for {
		start := time.Now()
		events, err := u.Run(ctx)
		// Also when stopping: updates done before the signal must be reported.
		notifier.Flush()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("cycle failed", "err", err)
		}
		if !errors.Is(err, context.Canceled) {
			m.Cycle(start, events, err)
		}
		if *once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(nextCycle(*interval, u.NextWindow())):
		}
	}
}

func defaultDockerConfig() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return filepath.Join(d, "config.json")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".docker", "config.json")
	}
	return "/config.json"
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// nextCycle waits the interval, or less if a deferred update's maintenance
// window opens sooner: a window shorter than the interval is never missed,
// and updates start right when their window opens.
func nextCycle(interval time.Duration, window time.Time) time.Duration {
	if window.IsZero() {
		return interval
	}
	if d := time.Until(window) + time.Second; d < interval {
		return max(d, time.Second)
	}
	return interval
}

// checkTZ reports a TZ value Go cannot load: a zone name, or a path to a
// zoneinfo file (with or without the leading ":"). POSIX rules such as
// CET-1CEST,M3.5.0,M10.5.0/3 are not supported by Go and fall back to UTC.
func checkTZ(tz string) error {
	tz = strings.TrimPrefix(tz, ":")
	if tz == "" {
		return nil
	}
	if strings.HasPrefix(tz, "/") {
		raw, err := os.ReadFile(tz) //nolint:gosec // the operator's own TZ setting
		if err == nil {
			_, err = time.LoadLocationFromTZData(tz, raw)
		}
		if err != nil {
			return fmt.Errorf("TZ=%q: %w", tz, err)
		}
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("TZ=%q is not a known time zone (use a name like Europe/Berlin): %w", tz, err)
	}
	return nil
}

// kumaTargets is the Uptime Kuma to put monitors into maintenance in, if
// configured. The password comes from a file or the environment, never a
// flag (it would show in the process list).
func kumaTargets(url, user, passFile string) ([]kuma.Target, error) {
	if url == "" {
		return nil, nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("--kuma-url: %q is not an http(s) URL", url)
	}
	pass := os.Getenv("NEPTIDE_KUMA_PASSWORD")
	if passFile != "" {
		raw, err := os.ReadFile(passFile)
		if err != nil {
			return nil, fmt.Errorf("--kuma-password-file: %w", err)
		}
		pass = strings.TrimRight(string(raw), "\r\n")
	}
	if user == "" || pass == "" {
		return nil, errors.New("--kuma-url needs --kuma-username and a password (--kuma-password-file or NEPTIDE_KUMA_PASSWORD)")
	}
	return []kuma.Target{{Name: url, URL: strings.TrimRight(url, "/"), Username: user, Password: pass}}, nil
}
