package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/handlers"
)

func init() {
	register(Command{
		Name: "web",
		Run:  runWeb,
	})
}

func runWeb(cfg *config.Config, database *db.DB, args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	port := fs.String("port", "", "")

	if err := fs.Parse(args); err != nil {
		return err
	}

	finalPort := cfg.Port
	if *port != "" {
		finalPort = *port
	}

	if err := os.MkdirAll(cfg.ReposPath, 0o755); err != nil {
		return err
	}

	templates, err := handlers.LoadTemplates()
	if err != nil {
		return err
	}

	staticFS, err := handlers.NewStaticFS()
	if err != nil {
		return err
	}

	app := &handlers.App{
		Config:    cfg,
		DB:        database,
		Templates: templates,
		StaticFS:  staticFS,
	}

	router := handlers.SetupRouter(app)

	srv := &http.Server{
		Addr:         ":" + finalPort,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errChan := make(chan error, 1)

	go func() {
		slog.Info("web server starting", "port", finalPort)
		errChan <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {

	case err := <-errChan:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}

	case sig := <-stop:
		slog.Info("shutdown", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(ctx)
	}

	return nil
}
