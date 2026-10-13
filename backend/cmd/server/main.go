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
	"strings"
	"text/tabwriter"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"codeberg.org/bvigh/geogallery/internal/config"
	"codeberg.org/bvigh/geogallery/internal/db"
	"codeberg.org/bvigh/geogallery/internal/handlers"
	"codeberg.org/bvigh/geogallery/internal/httplog"
	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	// Setup logging first so all startup errors are structured. Logs go to stderr, so
	// subcommand output on stdout stays parseable.
	setupLogger(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))

	if len(os.Args) > 1 {
		os.Exit(runSubcommand(os.Args[1], os.Args[2:], os.Stdout))
	}
	serve()
}

// setup loads config, connects to the database and applies migrations.
func setup(ctx context.Context) (*config.Config, *pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("config load failed: %w", err)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("db connect failed: %w", err)
	}
	migrationsPath := envOr("MIGRATIONS_PATH", "./migrations")
	if err := db.RunMigrations(cfg.DatabaseURL, migrationsPath); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrations failed: %w", err)
	}
	slog.Info("migrations applied")
	return cfg, pool, nil
}

func runSubcommand(name string, args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var run func(ctx context.Context, cfg *config.Config, store auth.Store) error
	check := func() error { return nil }

	switch name {
	case "invite":
		admin := fs.Bool("admin", false, "invite an admin; claims the unclaimed Owner account if there is one")
		ttl := fs.Duration("ttl", 72*time.Hour, "how long the invite link stays valid")
		run = func(ctx context.Context, cfg *config.Config, store auth.Store) error {
			url, claim, err := auth.IssueCLIInvite(ctx, store, cfg.PublicOrigin, *admin, *ttl)
			if err != nil {
				return err
			}
			slog.Info("invite created", "admin", *admin, "claims_user_id", claim, "expires_in", ttl.String())
			fmt.Fprintln(stdout, url)
			return nil
		}
	case "rebind-issuer":
		from := fs.String("from", "", "current issuer stored on users (exact string)")
		to := fs.String("to", "", "new issuer (must equal OIDC_ISSUER_URL byte for byte)")
		check = func() error {
			if *from == "" || *to == "" || *from == *to {
				return errors.New("--from and --to are required and must differ")
			}
			return nil
		}
		run = func(ctx context.Context, _ *config.Config, store auth.Store) error {
			n, err := store.RebindIssuer(ctx, *from, *to)
			if err != nil {
				return err
			}
			slog.Info("issuer rebound", "from", *from, "to", *to, "users", n)
			fmt.Fprintln(stdout, n)
			return nil
		}
	case "users":
		run = func(ctx context.Context, _ *config.Config, store auth.Store) error {
			users, err := store.ListUsers(ctx)
			if err != nil {
				return err
			}
			return writeUsersTable(stdout, users)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (available: invite, rebind-issuer, users; no command starts the server)\n", name)
		return 2
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected arguments: %v\n", name, fs.Args())
		return 2
	}
	if err := check(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 2
	}
	ctx := context.Background()
	cfg, pool, err := setup(ctx)
	if err != nil {
		slog.Error(name+": setup failed", "err", err)
		return 1
	}
	defer pool.Close()
	if err := run(ctx, cfg, auth.NewPGStore(pool)); err != nil {
		slog.Error(name+" failed", "err", err)
		return 1
	}
	return 0
}

func writeUsersTable(w io.Writer, users []auth.UserRecord) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\trole\tstatus\temail\tdisplay_name")
	for _, u := range users {
		status := "active"
		switch {
		case !u.Claimed:
			status = "unclaimed"
		case u.Disabled:
			status = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.ID, u.Role, status, dash(u.Email), dash(u.DisplayName))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type routeHandlers struct {
	media      *handlers.MediaHandler
	trips      *handlers.TripHandler
	game       *handlers.GameHandler
	ingest     *handlers.IngestHandler
	gpsTracks  *handlers.GPSTrackHandler
	gpsMatcher *handlers.GPSMatcherHandler
	admin      *handlers.AdminHandler
}

func serve() {
	ctx := context.Background()
	cfg, pool, err := setup(ctx)
	if err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	s3Client, err := storage.NewClient(cfg)
	if err != nil {
		slog.Error("s3 client init failed", "err", err)
		os.Exit(1)
	}
	if err := s3Client.Ping(ctx); err != nil {
		slog.Error("s3 connectivity check failed", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket, "err", err)
		os.Exit(1)
	}
	slog.Info("s3 connected", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)

	discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	provider, err := auth.NewProvider(discoveryCtx, cfg)
	cancel()
	if err != nil {
		slog.Error("oidc provider init failed", "issuer", cfg.OIDCIssuerURL, "discovery", cfg.OIDCDiscoveryURL, "err", err)
		os.Exit(1)
	}
	slog.Info("oidc provider ready", "issuer", cfg.OIDCIssuerURL)

	cop := http.NewCrossOriginProtection()
	if err := cop.AddTrustedOrigin(cfg.PublicOrigin); err != nil {
		slog.Error("csrf trusted origin rejected", "origin", cfg.PublicOrigin, "err", err)
		os.Exit(1)
	}

	store := auth.NewPGStore(pool)
	authH := auth.New(store, provider, cfg)
	matchWindow := time.Duration(cfg.GPSMatchWindowHours) * time.Hour
	h := routeHandlers{
		media:      handlers.NewMediaHandler(pool, s3Client, matchWindow, cfg.TripGapHours),
		trips:      handlers.NewTripHandler(pool, cfg.TripGapHours),
		game:       handlers.NewGameHandler(pool, s3Client),
		ingest:     handlers.NewIngestHandler(pool, s3Client, cfg.IngestWatchDir, matchWindow, cfg.TripGapHours),
		gpsTracks:  handlers.NewGPSTrackHandler(pool, matchWindow),
		gpsMatcher: handlers.NewGPSMatcherHandler(pool, matchWindow),
		admin:      handlers.NewAdminHandler(store, cfg.PublicOrigin),
	}

	addr := fmt.Sprintf(":%s", cfg.ServerPort)
	slog.Info("server listening", "addr", addr, "public_origin", cfg.PublicOrigin)
	if err := http.ListenAndServe(addr, newRouter(cop, authH, h)); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func newRouter(cop *http.CrossOriginProtection, authH *auth.Auth, h routeHandlers) http.Handler {
	r := chi.NewRouter()
	r.Use(httplog.New(slog.Default()))
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Use(cop.Handler)
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"status":"ok"}`))
		})
		r.Get("/auth/login", authH.Login)
		r.Get("/auth/callback", authH.Callback)
		r.Post("/auth/logout", authH.Logout)

		r.Group(func(r chi.Router) {
			r.Use(authH.RequireUser)
			r.Get("/auth/me", authH.Me)

			r.Get("/media", h.media.List)
			r.Post("/media/upload", h.media.Upload)
			r.Get("/media/{id}/url", h.media.GetURL)
			r.Patch("/media/{id}/location", h.media.UpdateLocation)
			r.Patch("/media/{id}/view-settings", h.media.UpdateViewSettings)
			r.Get("/media/{id}/keyframes", h.media.GetKeyframes)
			r.Put("/media/{id}/keyframes", h.media.ReplaceKeyframes)

			r.Get("/trips", h.trips.List)
			r.Patch("/trips/{id}", h.trips.Update)

			r.Get("/game/round", h.game.NewRound)
			r.Post("/game/guess", h.game.SubmitGuess)

			r.Post("/ingest/scan", h.ingest.Scan)

			r.Post("/gps-tracks/import", h.gpsTracks.Import)
			r.Post("/gps-matcher/run", h.gpsMatcher.Run)

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireSession)
				r.Delete("/media/{id}", h.media.Delete) // irreversible (S3 object removed): never via token
				r.Delete("/trips/{id}", h.trips.Delete)
				r.Get("/tokens", authH.ListTokens)
				r.Post("/tokens", authH.CreateToken)
				r.Delete("/tokens/{id}", authH.RevokeToken)

				r.Group(func(r chi.Router) {
					r.Use(auth.RequireAdmin)
					r.Get("/admin/users", h.admin.ListUsers)
					r.Patch("/admin/users/{id}", h.admin.UpdateUser)
					r.Post("/admin/invites", h.admin.CreateInvite)
				})
			})
		})
	})
	return r
}

func setupLogger(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
