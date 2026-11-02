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
	"strings"
	"syscall"
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

// connectS3 builds the S3 client and checks the bucket is reachable, so a wrong S3_ENDPOINT
// fails at startup instead of on every file.
func connectS3(ctx context.Context, cfg *config.Config) (*storage.Client, error) {
	s3Client, err := storage.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("s3 client init failed: %w", err)
	}
	if err := s3Client.Ping(ctx); err != nil {
		return nil, fmt.Errorf("s3 connectivity check failed (endpoint %s, bucket %s): %w", cfg.S3Endpoint, cfg.S3Bucket, err)
	}
	slog.Info("s3 connected", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)
	return s3Client, nil
}

// cmdEnv is what a subcommand gets after shared setup. s3 is nil unless the command asked for it.
type cmdEnv struct {
	cfg   *config.Config
	pool  *pgxpool.Pool
	store auth.Store
	s3    *storage.Client
}

func runSubcommand(name string, args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var run func(ctx context.Context, env cmdEnv) error
	check := func() error { return nil }
	needS3 := false

	switch name {
	case "invite":
		admin := fs.Bool("admin", false, "invite an admin; claims the unclaimed Owner account if there is one")
		ttl := fs.Duration("ttl", 72*time.Hour, "how long the invite link stays valid")
		run = func(ctx context.Context, env cmdEnv) error {
			url, claim, err := auth.IssueCLIInvite(ctx, env.store, env.cfg.PublicOrigin, *admin, *ttl)
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
		run = func(ctx context.Context, env cmdEnv) error {
			n, err := env.store.RebindIssuer(ctx, *from, *to)
			if err != nil {
				return err
			}
			slog.Info("issuer rebound", "from", *from, "to", *to, "users", n)
			fmt.Fprintln(stdout, n)
			return nil
		}
	case "users":
		run = func(ctx context.Context, env cmdEnv) error {
			users, err := env.store.ListUsers(ctx)
			if err != nil {
				return err
			}
			return writeUsersTable(stdout, users)
		}
	case "scan":
		userID := fs.String("user", "", "id of the user whose library receives the files (see `server users`)")
		dir := fs.String("dir", "", "directory to scan (default INGEST_WATCH_DIR)")
		needS3 = true
		check = func() error {
			if *userID == "" {
				return errors.New("--user is required (find the id with `server users`)")
			}
			return nil
		}
		run = func(ctx context.Context, env cmdEnv) error {
			users, err := env.store.ListUsers(ctx)
			if err != nil {
				return err
			}
			scanDir := *dir
			if scanDir == "" {
				scanDir = env.cfg.IngestWatchDir
			}
			deps := handlers.NewIngestDeps(env.pool, env.s3,
				time.Duration(env.cfg.GPSMatchWindowHours)*time.Hour, env.cfg.TripGapHours)
			scan := func(ctx context.Context, dir, ownerID string) (handlers.ScanResult, error) {
				return handlers.RunIngestScan(ctx, deps, dir, ownerID)
			}
			return runScan(ctx, stdout, scan, users, *userID, scanDir)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (available: invite, rebind-issuer, users, scan; no command starts the server)\n", name)
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
	// Ctrl-C / SIGTERM cancel ctx; scan stops after the current file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, pool, err := setup(ctx)
	if err != nil {
		slog.Error(name+": setup failed", "err", err)
		return 1
	}
	defer pool.Close()
	env := cmdEnv{cfg: cfg, pool: pool, store: auth.NewPGStore(pool)}
	if needS3 {
		if env.s3, err = connectS3(ctx, cfg); err != nil {
			slog.Error(name+": setup failed", "err", err)
			return 1
		}
	}
	if err := run(ctx, env); err != nil {
		slog.Error(name+" failed", "err", err)
		return 1
	}
	return 0
}

// scanTargetError explains why userID cannot receive a scan, or returns nil.
func scanTargetError(users []auth.UserRecord, userID string) error {
	for _, u := range users {
		if u.ID != userID {
			continue
		}
		switch {
		case !u.Claimed:
			return errors.New("user is unclaimed: run `server invite --admin` and sign in first")
		case u.Disabled:
			return fmt.Errorf("user %s is disabled", userID)
		}
		return nil
	}
	return fmt.Errorf("unknown user %q (list users with `server users`)", userID)
}

type scanFunc func(ctx context.Context, dir, ownerID string) (handlers.ScanResult, error)

// runScan validates the target user, runs the scan and prints its counts on stdout. Any error,
// an interrupt or a non-zero failed count makes the command exit 1.
func runScan(ctx context.Context, stdout io.Writer, scan scanFunc, users []auth.UserRecord, userID, dir string) error {
	// Postgres emits lowercase UUIDs; the owner ID also becomes the S3 key prefix, so it must be canonical.
	userID = strings.ToLower(userID)
	if err := scanTargetError(users, userID); err != nil {
		return err
	}
	res, err := scan(ctx, dir, userID)
	fmt.Fprintf(stdout, "ingested=%d skipped=%d failed=%d\n", res.Ingested, res.Skipped, res.Failed)
	switch {
	case ctx.Err() != nil:
		return errors.New("interrupted; re-run scan to finish")
	case err != nil:
		return err
	case res.Failed > 0:
		return fmt.Errorf("%d file(s) failed to ingest; see the errors above", res.Failed)
	}
	return nil
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
	gpsTracks  *handlers.GPSTrackHandler
	gpsMatcher *handlers.GPSMatcherHandler
	albums     *handlers.AlbumHandler
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

	s3Client, err := connectS3(ctx, cfg)
	if err != nil {
		slog.Error("startup failed", "err", err)
		os.Exit(1)
	}

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
		gpsTracks:  handlers.NewGPSTrackHandler(pool, matchWindow),
		gpsMatcher: handlers.NewGPSMatcherHandler(pool, matchWindow),
		albums:     handlers.NewAlbumHandler(pool),
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

			r.Post("/gps-tracks/import", h.gpsTracks.Import)
			r.Post("/gps-matcher/run", h.gpsMatcher.Run)

			r.Get("/albums", h.albums.List)
			r.Post("/albums", h.albums.Create)
			r.Patch("/albums/{id}", h.albums.Update)
			r.Get("/albums/{id}/members", h.albums.ListMembers)
			r.Post("/albums/{id}/media", h.albums.AddMedia)
			r.Post("/albums/{id}/media/remove", h.albums.RemoveMedia)
			r.Get("/media/{id}/albums", h.albums.MediaAlbums)

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireSession)
				r.Delete("/media/{id}", h.media.Delete) // irreversible (S3 object removed): never via token
				r.Delete("/trips/{id}", h.trips.Delete)
				// These change who can read whose media, and the directory lists everyone's email:
				// never via token.
				r.Delete("/albums/{id}", h.albums.Delete)
				r.Post("/albums/{id}/members", h.albums.AddMember)
				r.Delete("/albums/{id}/members/{user_id}", h.albums.RemoveMember)
				r.Get("/users", h.albums.ListUsers)
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
