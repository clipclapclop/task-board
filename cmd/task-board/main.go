package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/clipclapclop/task-board/internal/backup"
	"github.com/clipclapclop/task-board/internal/model"
	"github.com/clipclapclop/task-board/internal/server"
	"github.com/clipclapclop/task-board/internal/store"
)

var revision = "development"

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func databasePath() string { return env("TASK_BOARD_DATABASE", ".generated/task-board.sqlite3") }

func openStore() (*store.Store, error) {
	path := databasePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	return store.Open(path)
}

func main() {
	// Database files, WAL files, and administrative state may contain API-token
	// hashes. Keep newly created files private even if a library requests a more
	// permissive mode.
	syscall.Umask(0o077)
	if err := run(os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "serve":
		return serve(args)
	case "actor":
		return actorCommand(args)
	case "token":
		return tokenCommand(args)
	case "backup":
		return backupCommand(args)
	case "healthcheck":
		return healthcheckCommand(args)
	case "version":
		fmt.Println(revision)
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func ensureDefault(st *store.Store) (model.Actor, error) {
	username := env("TASK_BOARD_DEFAULT_ACTOR", "chad")
	display := env("TASK_BOARD_DEFAULT_ACTOR_NAME", "Chad")
	return st.EnsureDefaultAdmin(context.Background(), username, display)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", env("TASK_BOARD_LISTEN", ":8080"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err = ensureDefault(st); err != nil {
		return err
	}
	publicURL := env("TASK_BOARD_PUBLIC_URL", "http://localhost:8080")
	allowed := env("TASK_BOARD_ALLOWED_HOST", "")
	if allowed == "" {
		if u, parseErr := http.NewRequest("GET", publicURL, nil); parseErr == nil {
			allowed = u.Host
		}
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	app, err := server.New(st, logger, server.Config{PublicURL: publicURL, AllowedHost: allowed, DefaultActorUsername: env("TASK_BOARD_DEFAULT_ACTOR", "chad"), SecureCookies: strings.HasPrefix(publicURL, "https://")})
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	logger.Info("server starting", "listen", *listen, "revision", revision)
	err = httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func actorCommand(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: task-board actor create [flags]")
	}
	fs := flag.NewFlagSet("actor create", flag.ContinueOnError)
	username := fs.String("username", "", "immutable username")
	name := fs.String("name", "", "display name")
	kind := fs.String("kind", "human", "human or service")
	role := fs.String("role", "member", "member or admin")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	a, err := st.CreateActor(context.Background(), model.Actor{Username: *username, DisplayName: *name, Kind: *kind, Role: *role, Active: true})
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", a.ID, a.Username)
	return nil
}

func tokenCommand(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: task-board token create --actor USERNAME --name NAME")
	}
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	username := fs.String("actor", "", "actor username")
	name := fs.String("name", "", "token name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	a, err := st.ActorByName(context.Background(), *username)
	if err != nil {
		return err
	}
	_, secret, err := st.CreateToken(context.Background(), a.ID, *name, nil)
	if err != nil {
		return err
	}
	fmt.Println(secret)
	return nil
}

func backupCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: task-board backup create|verify|rehearse")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
		dest := fs.String("destination", env("TASK_BOARD_BACKUP_ROOT", ".generated/backups"), "backup directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		path, err := backup.Create(context.Background(), st, *dest, revision)
		if err == nil {
			fmt.Println(path)
		}
		return err
	case "verify":
		if len(args) != 2 {
			return fmt.Errorf("usage: task-board backup verify ARCHIVE")
		}
		return backup.Verify(args[1])
	case "rehearse":
		if len(args) != 2 {
			return fmt.Errorf("usage: task-board backup rehearse ARCHIVE")
		}
		return backup.Rehearse(args[1])
	case "restore":
		if len(args) != 2 {
			return fmt.Errorf("usage: task-board backup restore ARCHIVE")
		}
		return backup.Restore(args[1], databasePath())
	default:
		return fmt.Errorf("unknown backup command %q", args[0])
	}
}

func healthcheckCommand(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	endpoint := fs.String("url", "http://127.0.0.1:8080/health/ready", "readiness URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(*endpoint)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned %s", res.Status)
	}
	return nil
}
