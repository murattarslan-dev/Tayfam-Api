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

// demoCommand writes the demo tayfas (issue #179, ADR-0023; the Keşfet set:
// issue #227, ADR-0035). The platform owner runs it by hand inside the
// running identity container, which already has the configuration, secrets
// and network permissions it needs (docs/runbooks/demo-verisi.md):
//
//	identity demo-seed -owner-ref-code TYF-XXXXXX
const demoCommand = "demo-seed"

// demoPurgeCommand removes demo data through the normal deletion paths
// (issue #227, ADR-0035): by default the Keşfet set, with -hepsi the closed
// set too; -dry-run only counts. Run by hand like demo-seed:
//
//	identity demo-purge [-hepsi] [-dry-run]
const demoPurgeCommand = "demo-purge"

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
	return withSeeder(demoCommand, stderr, func(ctx context.Context, log *slog.Logger, seeder *demo.Seeder) error {
		log.InfoContext(ctx, "demo verisi yazılıyor")
		rep, err := seeder.Run(ctx, *owner)
		if err != nil {
			return err
		}
		log.InfoContext(ctx, "demo verisi hazır", slog.Int("tayfas", len(rep.Tayfas)), slog.Int("discover_tayfas", len(rep.Discover)))

		var out strings.Builder
		fmt.Fprintf(&out, "Demo hesapları: %d yeni, %d zaten vardı\n", rep.UsersCreated, rep.UsersExisting)
		for _, t := range rep.Tayfas {
			fmt.Fprintf(&out, "%s  %s  rolünüz: %s%s\n", t.ID, t.Name, t.OwnerRole, readOnlyNote(t))
		}
		fmt.Fprintf(&out, "\nKeşfet demo tayfaları (panelde Keşfet sekmesinden onaylayın; üyesi değilsiniz):\n")
		for _, t := range rep.Discover {
			fmt.Fprintf(&out, "%s  %s  Keşfet: %s%s\n", t.ID, t.Name, discoverStateName(t.DiscoverState), readOnlyNote(t))
		}
		fmt.Fprintf(&out, "\nArkadaşlarınız: %d, engeller: %d", rep.Social.Friends, rep.Social.Blocks)
		if rep.Social.Skipped > 0 {
			fmt.Fprintf(&out, ", engel nedeniyle atlanan arkadaşlık: %d", rep.Social.Skipped)
		}
		out.WriteString("\n")
		if _, err := io.WriteString(stdout, out.String()); err != nil {
			return fmt.Errorf("çıktı: %w", err)
		}
		return nil
	})
}

func runDemoPurge(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(demoPurgeCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("hepsi", false, "kapalı mağaza demo tayfalarını ve hesaplarını da temizler (varsayılan: yalnızca Keşfet demo seti)")
	dryRun := fs.Bool("dry-run", false, "hiçbir şey silmeden ne silineceğini sayı olarak gösterir")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return errors.New("beklenmeyen argüman")
	}
	return withSeeder(demoPurgeCommand, stderr, func(ctx context.Context, _ *slog.Logger, seeder *demo.Seeder) error {
		rep, err := seeder.Purge(ctx, demo.PurgeOptions{All: *all, DryRun: *dryRun})
		if err != nil {
			return err
		}
		var out strings.Builder
		scope := "Keşfet demo seti"
		if *all {
			scope = "Keşfet demo seti ve kapalı demo tayfaları"
		}
		if *dryRun {
			fmt.Fprintf(&out, "Deneme (hiçbir şey silinmedi), kapsam: %s\n", scope)
		} else {
			fmt.Fprintf(&out, "Kapsam: %s\n", scope)
		}
		for _, t := range rep.Tayfas {
			fmt.Fprintf(&out, "%s  %s  %s\n", t.ID, t.Name, t.Outcome)
		}
		verb := "kapatıldı"
		if *dryRun {
			verb = "kapatılacak"
		}
		fmt.Fprintf(&out, "Tayfa: %d, demo hesabı %s: %d (arkadaşlıkları ve engelleri hesapla birlikte düşer)\n",
			len(rep.Tayfas), verb, rep.Accounts)
		if _, err := io.WriteString(stdout, out.String()); err != nil {
			return fmt.Errorf("çıktı: %w", err)
		}
		return nil
	})
}

func readOnlyNote(t demo.TayfaResult) string {
	if t.ReadOnly {
		return " (salt okunur, atlandı)"
	}
	return ""
}

// discoverStateName turns a TayfaDiscoverState name into the word the
// runbook uses.
func discoverStateName(s string) string {
	switch s {
	case "TAYFA_DISCOVER_STATE_PENDING":
		return "onay bekliyor"
	case "TAYFA_DISCOVER_STATE_LISTED":
		return "listede"
	case "TAYFA_DISCOVER_STATE_UNDER_REVIEW":
		return "incelemede"
	case "TAYFA_DISCOVER_STATE_REJECTED":
		return "reddedildi"
	case "":
		return "-"
	default:
		return strings.ToLower(strings.TrimPrefix(s, "TAYFA_DISCOVER_STATE_"))
	}
}

// withSeeder sets up what both demo commands need from identity's own
// configuration (the identity container's environment) and runs f.
func withSeeder(name string, stderr io.Writer, f func(context.Context, *slog.Logger, *demo.Seeder) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, demoTimeout)
	defer cancel()

	c := config.New(os.LookupEnv)
	env := c.OneOf("TAYFAM_ENV", "local", "local", "dev", "prod")
	log := logx.New(stderr, "identity-"+name, env, logx.ParseLevel(c.String("LOG_LEVEL", "info")))
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
	return f(ctx, log, seeder)
}
