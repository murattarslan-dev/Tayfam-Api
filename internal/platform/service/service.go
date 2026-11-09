// Package service runs a Tayfam service process: it loads common configuration,
// builds the logger, serves the operational endpoints (/livez, /readyz) and
// shuts every server down gracefully on SIGINT/SIGTERM.
//
// A service's main function only describes what it serves:
//
//	func main() {
//		service.Main("core", func(rt *service.Runtime) error {
//			// read config with rt.Config, register checks on rt.Health,
//			// add servers with rt.AddHTTPServer
//			return nil
//		})
//	}
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/config"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/health"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/logx"
)

// Runtime is what a service's setup function uses to configure itself.
type Runtime struct {
	Name   string
	Env    string
	Config *config.Loader
	Log    *slog.Logger
	Health *health.Health

	servers []*namedServer
	workers []namedWorker
	onStart []func()
	onDrain []func()
}

// namedWorker is a background job, such as the outbox relay.
type namedWorker struct {
	name string
	run  func(context.Context) error
}

// AddWorker registers a background job that starts once the servers listen
// and whose context is cancelled when the service starts draining. run must
// return when its context is done; an error before that stops the service.
func (rt *Runtime) AddWorker(name string, run func(ctx context.Context) error) {
	rt.workers = append(rt.workers, namedWorker{name: name, run: run})
}

// namedServer is any server the runtime starts and stops: HTTP or gRPC.
type namedServer struct {
	name     string
	addr     string
	serve    func(net.Listener) error
	shutdown func(context.Context) error
	ln       net.Listener
}

// AddHTTPServer registers an HTTP server that starts after setup and stops on shutdown.
// The handler is wrapped with request ID, panic recovery and access logging.
func (rt *Runtime) AddHTTPServer(name, addr string, h http.Handler) {
	h = httpx.Chain(h, httpx.RequestID(), httpx.AccessLog(rt.Log), httpx.Recover(rt.Log))
	rt.addHTTP(name, addr, h)
}

// addOpsServer registers the probe server. Probes run every few seconds, so
// they are not access-logged.
func (rt *Runtime) addOpsServer(addr string, h http.Handler) {
	rt.addHTTP("ops", addr, httpx.Chain(h, httpx.RequestID(), httpx.Recover(rt.Log)))
}

func (rt *Runtime) addHTTP(name, addr string, h http.Handler) {
	srv := newHTTPServer(addr, h, rt.Log)
	rt.servers = append(rt.servers, &namedServer{
		name:  name,
		addr:  addr,
		serve: srv.Serve,
		shutdown: func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	})
}

// NewGRPCServer returns a gRPC server configured with the shared interceptors
// (grpcx.NewServer). Register services on it, then pass it to AddGRPCServer.
func (rt *Runtime) NewGRPCServer(opts ...grpc.ServerOption) *grpc.Server {
	return grpcx.NewServer(rt.Log, opts...)
}

// AddGRPCServer registers a gRPC server. The standard health service is
// registered on it: SERVING once all servers listen, NOT_SERVING while
// draining. Shutdown waits for in-flight calls up to the shutdown timeout.
func (rt *Runtime) AddGRPCServer(name, addr string, srv *grpc.Server) {
	h := grpcx.RegisterHealth(srv)
	rt.onStart = append(rt.onStart, h.SetServing)
	rt.onDrain = append(rt.onDrain, h.SetDraining)
	rt.servers = append(rt.servers, &namedServer{
		name:  name,
		addr:  addr,
		serve: srv.Serve,
		shutdown: func(ctx context.Context) error {
			done := make(chan struct{})
			go func() {
				srv.GracefulStop()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				srv.Stop()
				<-done
				return ctx.Err()
			}
		},
	})
}

func newHTTPServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Setup configures a service on the given runtime.
type Setup func(rt *Runtime) error

// Main runs the service and exits the process with a non-zero code on failure.
func Main(name string, setup Setup) {
	if err := runWithSignals(name, setup); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func runWithSignals(name string, setup Setup) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, name, os.LookupEnv, os.Stdout, setup, nil)
}

// Run is Main without process concerns, for tests. When started is not nil it
// receives the bound address of every server once all of them are listening.
func Run(ctx context.Context, name string, lookup func(string) (string, bool), out io.Writer, setup Setup, started chan<- map[string]string) error {
	cfg := config.New(lookup)
	env := cfg.OneOf("TAYFAM_ENV", "local", "local", "dev", "prod")
	level := logx.ParseLevel(cfg.String("LOG_LEVEL", "info"))
	opsAddr := cfg.String("OPS_ADDR", ":8081")
	drainDelay := cfg.Duration("SHUTDOWN_DRAIN_DELAY", 0)
	shutdownTimeout := cfg.Duration("SHUTDOWN_TIMEOUT", 15*time.Second)

	log := logx.New(out, name, env, level)
	rt := &Runtime{Name: name, Env: env, Config: cfg, Log: log, Health: health.New(2 * time.Second)}

	ops := http.NewServeMux()
	rt.Health.Register(ops)
	ops.HandleFunc("/", httpx.NotFound)
	rt.addOpsServer(opsAddr, ops)

	if err := setup(rt); err != nil {
		return fmt.Errorf("kurulum başarısız: %w", err)
	}
	if err := cfg.Err(); err != nil {
		return fmt.Errorf("yapılandırma hatalı:\n%w", err)
	}

	// Bind every listener before serving so a port conflict fails startup cleanly.
	var lc net.ListenConfig
	for _, s := range rt.servers {
		ln, err := lc.Listen(ctx, "tcp", s.addr)
		if err != nil {
			closeListeners(rt.servers)
			return fmt.Errorf("%s sunucusu %s adresini dinleyemiyor: %w", s.name, s.addr, err)
		}
		s.ln = ln
	}

	errCh := make(chan error, len(rt.servers)+len(rt.workers))
	var wg sync.WaitGroup
	addrs := make(map[string]string, len(rt.servers))
	for _, s := range rt.servers {
		addrs[s.name] = s.ln.Addr().String()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
				errCh <- fmt.Errorf("%s sunucusu durdu: %w", s.name, err)
			}
		}()
	}
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	var workers sync.WaitGroup
	for _, w := range rt.workers {
		workers.Go(func() {
			if err := w.run(workerCtx); err != nil && workerCtx.Err() == nil {
				errCh <- fmt.Errorf("%s işi durdu: %w", w.name, err)
			}
		})
	}
	for _, f := range rt.onStart {
		f()
	}
	log.Info("servis başladı", slog.Any("adresler", addrs))
	if started != nil {
		started <- addrs
	}

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("kapanış sinyali alındı")
	case runErr = <-errCh:
		log.Error("sunucu hatası", slog.String("hata", runErr.Error()))
	}

	// Background jobs stop first; they finish their current step.
	stopWorkers()
	workers.Wait()
	// Fail readiness so traffic drains, then stop the servers.
	rt.Health.SetDraining()
	for _, f := range rt.onDrain {
		f()
	}
	if drainDelay > 0 && runErr == nil {
		time.Sleep(drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range rt.servers {
		if err := s.shutdown(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("%s sunucusu kapatılamadı: %w", s.name, err))
		}
	}
	wg.Wait()
	log.Info("servis durdu")
	return runErr
}

func closeListeners(servers []*namedServer) {
	for _, s := range servers {
		if s.ln != nil {
			_ = s.ln.Close()
		}
	}
}
