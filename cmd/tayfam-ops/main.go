// Command tayfam-ops is Tayfam Ops: a local web UI for the platform owner's
// secret tasks (issue #148, docs/runbooks/sops.md "Tayfam Ops ile").
//
//	make ops        (from the repository root, in WSL)
//
// It listens only on 127.0.0.1, prints a one-time link and, under WSL, opens
// it in the Windows default browser. It stops on Ctrl+C, when its window is
// closed, or after an hour without requests. Optional environment:
// TAYFAM_OPS_PORT (default: a free port), TAYFAM_OPS_NO_BROWSER=1.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/ops"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/logx"
)

const idleLimit = time.Hour

func main() {
	logger := logx.New(os.Stderr, "tayfam-ops", "local", slog.LevelInfo)
	if err := run(logger); err != nil {
		logger.Error("tayfam-ops", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.New(os.LookupEnv)
	port := cfg.Int("TAYFAM_OPS_PORT", 0)
	noBrowser := cfg.OneOf("TAYFAM_OPS_NO_BROWSER", "0", "0", "1") == "1"
	if err := cfg.Err(); err != nil {
		return err
	}

	repo, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "scripts", "new-r2-secret.sh")); err != nil {
		return errors.New("repository root bulunamadı: tayfam-ops repo kökünden çalıştırılır (make ops)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	ln, err := ops.Listen(ctx, fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	var open func(context.Context, string) error
	_, wslErr := exec.LookPath("wslpath")
	if wslErr == nil {
		open = ops.ExplorerOpener
	}
	srv, err := ops.New(ops.Config{
		Repo:    repo,
		Port:    ln.Addr().(*net.TCPAddr).Port,
		Logger:  logger,
		OpenDir: open,
	})
	if err != nil {
		_ = ln.Close()
		return err
	}

	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()

	url := srv.LaunchURL()
	fmt.Println("Tayfam Ops açık. Tarayıcı açılmazsa bu bağlantıyı bir kez kullanın:")
	fmt.Println("  " + url)
	fmt.Println("Kapatmak için bu pencereyi kapatın ya da Ctrl+C.")
	if wslErr == nil && !noBrowser {
		if err := ops.WindowsOpen(ctx, url); err != nil {
			logger.Warn("tarayıcı açılamadı", "err", err)
		}
	}

	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve: %w", err)
		case <-tick.C:
			if srv.Idle() < idleLimit {
				continue
			}
			logger.Info("bir saattir istek yok; kapanıyor")
		case <-ctx.Done():
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := hs.Shutdown(shutdown)
		cancel()
		if err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
