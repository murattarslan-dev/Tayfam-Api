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
	"strings"
	"syscall"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/demo"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/logx"
)

// demoCommand writes the demo tayfas once (issue #179, ADR-0023). The
// platform owner runs it by hand inside the running identity container,
// which already has the configuration, secrets and network permissions it
// needs (docs/runbooks/demo-verisi.md):
//
//	identity demo-seed -owner-ref-code TYF-XXXXXX
const demoCommand = "demo-seed"

// demoTokenTTL is the lifetime of the access tokens the demo accounts and
// the owner call core and chat with; each call gets a new one.
const demoTokenTTL = 5 * time.Minute

// demoTimeout bounds the whole run.
const demoTimeout = 15 * time.Minute

func runDemoSeed(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(demoCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	owner := fs.String("owner-ref-code", "", "platform sahibinin referans kodu (TYF-XXXXXX); hesap demo tayfalarına eklenir")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *owner == "" || fs.NArg() > 0 {
		fs.Usage()
		return errors.New("-owner-ref-code zorunludur")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, demoTimeout)
	defer cancel()

	c := config.New(os.LookupEnv)
	env := c.OneOf("TAYFAM_ENV", "local", "local", "dev", "prod")
	log := logx.New(stderr, "identity-"+demoCommand, env, logx.ParseLevel(c.String("LOG_LEVEL", "info")))
	signing := c.Required("AUTH_SIGNING_KEY")
	retired := c.String("AUTH_RETIRED_KEYS", "")
	birthdateKeys := c.Required("IDENTITY_BIRTHDATE_KEYS")
	issuer := c.Required("AUTH_ISSUER")
	audience := c.Required("AUTH_AUDIENCE")
	coreAddr := c.Required("CORE_GRPC_ADDR")
	chatAddr := c.Required("CHAT_GRPC_ADDR")
	dbCfg := db.LoadConfig(c)
	if err := c.Err(); err != nil {
		return fmt.Errorf("yapılandırma hatalı:\n%w", err)
	}

	ring, err := keys.Parse(signing, retired)
	if err != nil {
		return err
	}
	cipher, err := birthdate.ParseKeys(birthdateKeys)
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(auth.SignerConfig{Key: ring.Active(), Issuer: issuer, Audience: audience, TTL: demoTokenTTL})
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dbCfg.URL())
	if err != nil {
		return err
	}
	defer pool.Close()
	// Tokens are attached per call (auth.WithToken); the connections carry
	// no identity of their own.
	coreConn, err := grpcx.NewClient(coreAddr)
	if err != nil {
		return err
	}
	defer func() { _ = coreConn.Close() }()
	chatConn, err := grpcx.NewClient(chatAddr)
	if err != nil {
		return err
	}
	defer func() { _ = chatConn.Close() }()

	seeder, err := demo.New(demo.Config{
		Pool: pool, Cipher: cipher, Log: log, Tokens: signer,
		Core: demo.NewCore(coreConn), Chat: demo.NewChannelClient(chatConn),
	})
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "demo verisi yazılıyor")
	rep, err := seeder.Run(ctx, *owner)
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "demo verisi hazır", slog.Int("tayfas", len(rep.Tayfas)))

	var out strings.Builder
	fmt.Fprintf(&out, "Demo hesapları: %d yeni, %d zaten vardı\n", rep.UsersCreated, rep.UsersExisting)
	for _, t := range rep.Tayfas {
		note := ""
		if t.ReadOnly {
			note = " (salt okunur, atlandı)"
		}
		fmt.Fprintf(&out, "%s  %s  rolünüz: %s%s\n", t.ID, t.Name, t.OwnerRole, note)
	}
	if _, err := io.WriteString(stdout, out.String()); err != nil {
		return fmt.Errorf("çıktı: %w", err)
	}
	return nil
}
