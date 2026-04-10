package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/handlers"
	sshhandler "github.com/mmrzaf/gitman/internal/ssh"
)

func main() {
	// Setup modern structured logging (Go 1.21+)

	cfg := config.LoadConfig()

	level := config.ParseLogLevel(cfg.LogLevel)

	logger := slog.New(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		}),
	)

	slog.SetDefault(logger)

	// Delegate to a run function so deferred statements (like db.Close)
	// are guaranteed to execute before os.Exit terminates the process.
	if err := run(os.Args, os.Stdout); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}

// run contains the core startup logic and returns an error instead of using log.Fatal.
func run(args []string, out io.Writer) error {
	if len(args) < 2 {
		printHelp(out)
		return nil // Or return an error if you want to exit with code 1
	}

	cfg := config.LoadConfig()

	// Initialize Database
	database, err := db.InitDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	// This will now reliably execute when run() returns.
	defer func() {
		if err := database.Close(); err != nil {
			slog.Error("failed to close database cleanly", "error", err)
		}
	}()

	cmd := args[1]

	// Route subcommands
	switch cmd {
	case "serve":
		return runSSH(cfg, database, args[2:])
	case "web":
		return runWeb(cfg, database, args[2:])
	case "help", "-h", "--help":
		printHelp(out)
		return nil
	default:
		if _, err := fmt.Fprintf(out, "Unknown command: %s\n\n", cmd); err != nil {
			return err
		}
		printHelp(out)
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func runSSH(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: gitman serve <keyID>")
	}

	keyID := args[0]

	// Assuming Serve blocks. If it returns an error in your actual code, return it here.
	sshhandler.Serve(keyID, cfg, database)
	return nil
}

func runWeb(cfg *config.Config, database *db.DB, args []string) error {
	webCmd := flag.NewFlagSet("web", flag.ContinueOnError)
	port := webCmd.String("port", "", "override web server port")

	if err := webCmd.Parse(args); err != nil {
		return fmt.Errorf("failed to parse web flags: %w", err)
	}

	finalPort := cfg.Port
	if *port != "" {
		finalPort = *port
	}

	// Setup application dependencies
	if err := os.MkdirAll(cfg.ReposPath, 0o755); err != nil {
		return fmt.Errorf("failed to create repos directory: %w", err)
	}

	templates, err := handlers.LoadTemplates()
	if err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}

	staticFS, err := handlers.NewStaticFS()
	if err != nil {
		return fmt.Errorf("failed to load static files: %w", err)
	}

	app := &handlers.App{
		Config:    cfg,
		DB:        database,
		Templates: templates,
		StaticFS:  staticFS,
	}

	router := handlers.SetupRouter(app)

	srv := &http.Server{
		Addr:    ":" + finalPort,
		Handler: router,
		// Good practice to add timeouts to prevent slowloris attacks
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Channel to listen for errors coming from the listener.
	serverErrors := make(chan error, 1)

	go func() {
		slog.Info("Gitman web server starting", "url", fmt.Sprintf("http://localhost:%s", finalPort))
		serverErrors <- srv.ListenAndServe()
	}()

	// Channel to listen for an interrupt or terminate signal from the OS.
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Wait for either a fatal server error or a shutdown signal
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil

	case sig := <-shutdown:
		slog.Info("shutdown signal received", "signal", sig)

		// Create context with timeout for the graceful shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			// Try forcefully closing if graceful shutdown fails
			_ = srv.Close()
			return fmt.Errorf("could not stop server gracefully: %w", err)
		}

		slog.Info("server stopped gracefully")
		return nil
	}
}

func printHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, `Gitman - Lightweight Git Server

Usage:
  gitman <command> [options]

Commands:
  web                 Start the web interface
  serve <keyID>       SSH git handler (internal use)

Options (web):
  --port <port>       Override configured web port

Examples:
  gitman web
  gitman web --port 8081
  gitman serve 42`)
}
