// Package db opens a service's own Postgres database and runs its
// migrations (ADR-0002, ADR-0006). Each service connects with its own user,
// which pg_hba and REVOKE statements limit to that service's database.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
)

// Config is a database connection target. The service connects as its
// runtime user (<service>_app, secrets/<service>-app-db); only the migrate
// init container uses the owner (<service>, secrets/<service>-db), see
// ADR-0015.
type Config struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	// SSLMode is "require" in clusters: traffic is encrypted. Server
	// certificate verification (verify-full) needs the CloudNativePG CA
	// distributed without its private key; tracked in ADR-0011.
	SSLMode string
}

// LoadConfig reads DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD and
// DB_SSLMODE. Host, name, user and password are required.
func LoadConfig(c *config.Loader) Config {
	return Config{
		Host:     c.Required("DB_HOST"),
		Port:     c.Int("DB_PORT", 5432),
		Name:     c.Required("DB_NAME"),
		User:     c.Required("DB_USER"),
		Password: c.Required("DB_PASSWORD"),
		SSLMode:  c.OneOf("DB_SSLMODE", "require", "require", "verify-full", "disable"),
	}
}

// URL returns the connection string, password included. It is passed only
// to the driver and never logged.
func (c Config) URL() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   c.Host + ":" + strconv.Itoa(c.Port),
		Path:   "/" + c.Name,
	}
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	q.Set("connect_timeout", "5")
	u.RawQuery = q.Encode()
	return u.String()
}

// Open returns a connection pool. Every session has a statement timeout so a
// stuck query cannot hold connections forever.
func Open(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		// The parse error may contain the connection string; do not wrap it.
		return nil, errors.New("veritabanı bağlantı ayarları çözümlenemedi")
	}
	cfg.MaxConns = 10
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("veritabanı havuzu: %w", err)
	}
	return pool, nil
}

// Ping is a readiness check.
func Ping(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("veritabanı: %w", err)
		}
		return nil
	}
}

// Migrate applies pending migrations from fsys (goose SQL files). A Postgres
// advisory lock ensures only one migration runs at a time, even when several
// pods start together (ADR-0006).
func Migrate(ctx context.Context, log *slog.Logger, connString string, fsys fs.FS) error {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return errors.New("veritabanı bağlantı ayarları çözümlenemedi")
	}
	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	defer func() { _ = sqlDB.Close() }()
	return migrate(ctx, log, sqlDB, fsys)
}

func migrate(ctx context.Context, log *slog.Logger, sqlDB *sql.DB, fsys fs.FS) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migration kilidi: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("migration sağlayıcısı: %w", err)
	}
	results, err := p.Up(ctx)
	for _, r := range results {
		log.InfoContext(ctx, "migration uygulandı",
			slog.String("file", r.Source.Path),
			slog.Duration("duration", r.Duration))
	}
	if err != nil {
		return fmt.Errorf("migration: %w", err)
	}
	if len(results) == 0 {
		log.InfoContext(ctx, "migration yok, şema güncel")
	}
	return nil
}
