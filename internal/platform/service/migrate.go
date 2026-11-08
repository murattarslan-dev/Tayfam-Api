package service

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/logx"
)

// MigrateCommand is the subcommand that applies migrations and exits. It
// runs as an init container before the service starts (ADR-0006).
const MigrateCommand = "migrate"

// MainWithMigrations runs `<binary> migrate` as a one-shot migration and
// anything else as the service.
func MainWithMigrations(name string, migrations fs.FS, setup Setup) {
	if len(os.Args) > 1 && os.Args[1] == MigrateCommand {
		if err := runMigrations(name, migrations); err != nil {
			fmt.Fprintf(os.Stderr, "%s migrate: %v\n", name, err)
			os.Exit(1)
		}
		return
	}
	Main(name, setup)
}

func runMigrations(name string, migrations fs.FS) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cfg := config.New(os.LookupEnv)
	env := cfg.OneOf("TAYFAM_ENV", "local", "local", "dev", "prod")
	log := logx.New(os.Stdout, name+"-migrate", env, logx.ParseLevel(cfg.String("LOG_LEVEL", "info")))
	dbCfg := db.LoadConfig(cfg)
	if err := cfg.Err(); err != nil {
		return fmt.Errorf("yapılandırma hatalı:\n%w", err)
	}
	log.InfoContext(ctx, "migration başlıyor", "target", dbCfg.String())
	return db.Migrate(ctx, log, dbCfg.URL(), migrations)
}
