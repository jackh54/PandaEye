// Command pandaeye is the PandaEye cloud: one binary, one subcommand per role (ARCHITECTURE 5.1).
//
//	pandaeye all|gateway|worker|verdict|api [--config file.yaml]
//	pandaeye migrate [--config file.yaml]
//	pandaeye replay --tenant t_... (--player uuid --since 2h [--until RFC3339] | --clip id)
//	pandaeye version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/api"
	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/gateway"
	"github.com/jackh54/PandaEye/cloud/internal/store/sqlstore"
	"github.com/jackh54/PandaEye/cloud/internal/verdict"
	"github.com/jackh54/PandaEye/cloud/internal/worker"
)

// version is set at build time: go build -ldflags "-X main.version=1.2.3" ./cmd/pandaeye
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

const usage = `usage: pandaeye <command> [flags]

commands:
  all       embedded NATS + gateway + worker + verdict + api in one process (the only sqlite mode)
  gateway   plugin WebSocket gateway
  worker    partition workers (cloud checks)
  verdict   verdict service and store writer
  api       REST API, metering, janitor
  migrate   apply database migrations
  replay    re-run checks over stored stream data or a clip and print the signals
  version   print the version

Configuration: PE_* environment variables and an optional --config YAML file (ARCHITECTURE 5.1).
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		fmt.Fprintf(stdout, "pandaeye %s (%s, %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	role := config.Role(cmd)
	if !role.Valid() {
		fmt.Fprintf(stderr, "pandaeye: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	fs := flag.NewFlagSet("pandaeye "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "YAML configuration file (keys: PE_ names in lower case with dots)")
	var rf replayFlags
	if role == config.RoleReplay {
		rf.register(fs)
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pandaeye %s: unexpected arguments %q\n", cmd, fs.Args())
		return 2
	}
	cfg, err := config.Load(role, *cfgPath, environ)
	if err != nil {
		fmt.Fprintln(stderr, "pandaeye:", err)
		return 1
	}
	log := app.NewLogger(stderr, cfg.LogLevel)
	switch role {
	case config.RoleMigrate:
		err = migrate(ctx, cfg, stdout)
	case config.RoleReplay:
		err = replay(ctx, cfg, rf, log, stdout, stderr)
	default:
		err = serve(ctx, cfg, log)
	}
	if err != nil {
		log.Error("pandaeye "+cmd+" failed", "err", err)
		return 1
	}
	return 0
}

// serve builds the role's services and runs them until a signal arrives.
func serve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	d, err := app.New(ctx, cfg, app.Options{Version: version, Logger: log})
	if err != nil {
		return err
	}
	services, err := buildServices(cfg.Role, d)
	if err != nil {
		d.Close()
		return err
	}
	log.Info("pandaeye starting", "role", string(cfg.Role), "version", version, "config", cfg)
	return app.Run(ctx, d, services...)
}

// buildServices constructs the services of a role. `all` runs every service in one process.
func buildServices(role config.Role, d *app.Deps) ([]app.Named, error) {
	type ctor struct {
		role config.Role
		new  func(*app.Deps) (app.Service, error)
	}
	ctors := []ctor{
		{config.RoleGateway, func(d *app.Deps) (app.Service, error) { return gateway.New(d) }},
		{config.RoleWorker, func(d *app.Deps) (app.Service, error) { return worker.New(d) }},
		{config.RoleVerdict, func(d *app.Deps) (app.Service, error) { return verdict.New(d) }},
		{config.RoleAPI, func(d *app.Deps) (app.Service, error) { return api.New(d) }},
	}
	var out []app.Named
	for _, c := range ctors {
		if role != config.RoleAll && role != c.role {
			continue
		}
		s, err := c.new(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.role, err)
		}
		out = append(out, app.Named{Name: string(c.role), Service: s})
	}
	return out, nil
}

func migrate(ctx context.Context, cfg *config.Config, stdout io.Writer) error {
	st, err := sqlstore.Open(ctx, cfg.DB.Driver, cfg.DB.DSN.Reveal(), sqlstore.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.Migrate(ctx)
	if err != nil {
		return err
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied %d migration(s); schema version %d\n", n, v)
	return nil
}

type replayFlags struct {
	tenant, player, since, until, clip string
}

func (r *replayFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&r.tenant, "tenant", "", "tenant id (required)")
	fs.StringVar(&r.player, "player", "", "player UUID")
	fs.StringVar(&r.since, "since", "1h", "start: RFC 3339 time or a duration ago (e.g. 2h)")
	fs.StringVar(&r.until, "until", "", "end: RFC 3339 time or a duration ago (default now)")
	fs.StringVar(&r.clip, "clip", "", "replay a stored clip instead of stream data")
}

func (r replayFlags) options(now time.Time) (worker.ReplayOptions, error) {
	o := worker.ReplayOptions{TenantID: r.tenant, Player: r.player, ClipID: r.clip, Until: now}
	if r.tenant == "" {
		return o, errors.New("--tenant is required")
	}
	if r.clip == "" && r.player == "" {
		return o, errors.New("--player or --clip is required")
	}
	var err error
	if o.Since, err = parseWhen(r.since, now); err != nil {
		return o, fmt.Errorf("--since: %w", err)
	}
	if r.until != "" {
		if o.Until, err = parseWhen(r.until, now); err != nil {
			return o, fmt.Errorf("--until: %w", err)
		}
	}
	if !o.Since.Before(o.Until) {
		return o, errors.New("--since must be before --until")
	}
	return o, nil
}

// parseWhen accepts an RFC 3339 time or a duration before now ("90m", "2d").
func parseWhen(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d, err := config.ParseDuration(strings.TrimPrefix(s, "-"))
	if err != nil {
		return time.Time{}, fmt.Errorf("want an RFC 3339 time or a duration, got %q", s)
	}
	return now.Add(-d), nil
}

func replay(ctx context.Context, cfg *config.Config, rf replayFlags, log *slog.Logger, stdout, stderr io.Writer) error {
	o, err := rf.options(time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "pandaeye replay:", err)
		return err
	}
	d, err := app.New(ctx, cfg, app.Options{Version: version, Logger: log})
	if err != nil {
		return err
	}
	defer d.Close()
	return worker.Replay(ctx, d, o, stdout)
}
