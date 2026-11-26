// Command q8-relay runs Relay: it watches Radarr, Sonarr and Lidarr, and
// tells a paired Q8 within seconds of a file being imported, serving the file itself
// over HTTP. In "Connect what you have" it only reads: nothing on the
// stack changes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/q8-app/q8-relay/internal/announce"
	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/config"
	"github.com/q8-app/q8-relay/internal/focused"
	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/pairing"
	"github.com/q8-app/q8-relay/internal/server"
)

// version is set at build time: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version":
			fmt.Println(version)
			return nil
		case "-healthcheck", "--healthcheck":
			return healthcheck()
		}
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("Relay can't use its data folder %s: %w", cfg.DataDir, err)
	}
	h, err := hub.Open(cfg.DataDir)
	if err != nil {
		log.Warn(err.Error())
	}
	pair, err := pairing.Open(cfg.DataDir, func(code string) {
		log.Info("pairing code — type it in Q8's Connect to Relay…", "code", code)
	})
	if err != nil {
		return err
	}
	if len(cfg.Apps) == 0 {
		log.Warn("no app set: give Relay RADARR_URL and RADARR_API_KEY (or Sonarr's, Lidarr's)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var watchers []*arr.Watcher
	var clients []*arr.Client
	for _, app := range cfg.Apps {
		client := &arr.Client{App: app.Name, BaseURL: app.BaseURL, APIKey: app.APIKey}
		w := &arr.Watcher{Client: client, Hub: h, PathMaps: cfg.PathMaps, Dir: cfg.DataDir, Log: log}
		if err := w.Load(); err != nil {
			log.Warn(err.Error())
		}
		check, cancelCheck := context.WithTimeout(ctx, 15*time.Second)
		if name, v, err := client.Status(check); err != nil {
			log.Warn("can't reach the app yet — Relay keeps trying", "app", app.Name, "err", err)
		} else {
			log.Info("connected", "app", name, "version", v)
		}
		cancelCheck()
		watchers = append(watchers, w)
		clients = append(clients, client)
		go w.Run(ctx, cfg.PollInterval)
	}

	focus, err := focused.Open(cfg.DataDir, pair.RelayID(), clients, h, log)
	if err != nil {
		log.Warn(err.Error())
	}
	if focus.Enabled() {
		log.Info("Q8 focused is on: once Q8 confirms a file, the arr's copy is deleted into its Recycle Bin")
	}
	stats, err := server.OpenStats(cfg.DataDir)
	if err != nil {
		log.Warn(err.Error())
	}
	board := arr.NewBoard()
	go board.Run(ctx, clients, 5*time.Minute)
	srv := &server.Server{Name: cfg.Name, Version: version, ShowCode: cfg.ShowCode, Hub: h, Pairing: pair,
		Watchers: watchers, Focused: focus, Log: log, Health: board, Stats: stats, Started: time.Now()}
	if !cfg.Announce {
		srv.Bonjour = "Bonjour is switched off (RELAY_ANNOUNCE=false)"
	}
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.Announce {
		stop, err := announce.Start(cfg.Name, pair.RelayID(), cfg.Port, server.Protocol)
		if err != nil {
			log.Warn("Bonjour announcing failed — Q8 can still connect by address", "err", err)
			srv.Bonjour = "Bonjour couldn't start"
		}
		defer stop()
	}
	pair.Code() // printed by the callback, so the log has one from the start
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = httpServer.Shutdown(shutdown)
	}()
	log.Info("Relay is listening", "port", cfg.Port, "name", cfg.Name, "version", version)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck asks this Relay's own info, for Docker's HEALTHCHECK: the
// image has no curl.
func healthcheck() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/info", cfg.Port))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("info answered %d", resp.StatusCode)
	}
	return nil
}
