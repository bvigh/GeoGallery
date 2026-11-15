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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/db"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/httplog"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
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
		tz := fs.String("tz", "", "IANA zone the files' camera clocks show, e.g. Asia/Tokyo (default: the user's camera zone setting, else CAMERA_TIMEZONE)")
		var zone *time.Location
		needS3 = true
		check = func() error {
			if *userID == "" {
				return errors.New("--user is required (find the id with `server users`)")
			}
			if *tz != "" {
				var err error
				if zone, err = ingest.LoadZone(*tz); err != nil {
					return fmt.Errorf("--tz: %w", err)
				}
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
			logCameraTimezone(env.cfg)
			deps := handlers.NewIngestDeps(env.pool, env.s3,
				time.Duration(env.cfg.GPSMatchWindowHours)*time.Hour, env.cfg.TripGapHours, cameraZone(env.cfg))
			scan := func(ctx context.Context, dir, ownerID string) (handlers.ScanResult, error) {
				return handlers.RunIngestScan(ctx, deps, dir, ownerID, zone)
			}
			return runScan(ctx, stdout, scan, users, *userID, scanDir)
		}
	case "retime":
		userID := fs.String("user", "", "id of the user whose media are re-read (see `server users`)")
		apply := fs.Bool("apply", false, "write the changes; without it the command only lists them")
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
			logCameraTimezone(env.cfg)
			deps := handlers.NewRetimeDeps(env.pool, env.s3,
				time.Duration(env.cfg.GPSMatchWindowHours)*time.Hour, env.cfg.TripGapHours, cameraZone(env.cfg))
			retime := func(ctx context.Context, ownerID string, report func(handlers.RetimeChange)) (handlers.RetimeResult, error) {
				return handlers.RunRetime(ctx, deps, ownerID, *apply, report)
			}
			return runRetime(ctx, stdout, retime, users, *userID, *apply)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (available: invite, rebind-issuer, users, scan, retime; no command starts the server)\n", name)
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

// runScan validates the target user, runs the scan and prints its counts on stdout, then how the
// ingested files' capture times were read. Any error, an interrupt or a non-zero failed count makes
// the command exit 1.
func runScan(ctx context.Context, stdout io.Writer, scan scanFunc, users []auth.UserRecord, userID, dir string) error {
	// Postgres emits lowercase UUIDs; the owner ID also becomes the S3 key prefix, so it must be canonical.
	userID = strings.ToLower(userID)
	if err := scanTargetError(users, userID); err != nil {
		return err
	}
	res, err := scan(ctx, dir, userID)
	fmt.Fprintf(stdout, "ingested=%d skipped=%d failed=%d\n", res.Ingested, res.Skipped, res.Failed)
	writeTimeReport(stdout, res.TimeReport, userID)
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

// writeTimeReport prints how the ingested files were dated: by source, then EXIF times by the rule
// that gave their offset, with the camera zone and who named it. It warns when EXIF times were read in
// a camera zone that is UTC all year and that the scan did not name (--tz), which few camera clocks
// show, and says how to repair them.
func writeTimeReport(w io.Writer, r handlers.TimeReport, userID string) {
	src, zone := r.BySource, r.ByZone
	fmt.Fprintf(w, "by_source: exif_original=%d exif_ifd0=%d container=%d mtime=%d none=%d\n",
		src.ExifOriginal, src.ExifIFD0, src.Container, src.Mtime, src.None)
	fmt.Fprintf(w, "by_zone: gps=%d clock_rule=%d offset=%d camera=%d camera_zone=%q camera_zone_from=%s\n",
		zone.GPS, zone.ClockRule, zone.Offset, zone.Camera, r.CameraZone, r.CameraZoneFrom)
	if r.AssumedZoneIsUTC {
		set := "set CAMERA_TIMEZONE to it (e.g. Europe/Warsaw) or the user's camera zone in Settings"
		if r.CameraZoneFrom == ingest.CameraZoneFromUser {
			set = "change the user's camera zone in Settings"
		}
		fmt.Fprintf(w, "warning: the EXIF times of %d file(s) were read in camera zone %q, which is UTC all year. "+
			"If your camera clocks show another zone, %s, "+
			"then run `server retime --user %s` to list the times that would move, and again with --apply to move them.\n",
			zone.Camera, r.CameraZone, set, userID)
	}
}

type retimeFunc func(ctx context.Context, ownerID string, report func(handlers.RetimeChange)) (handlers.RetimeResult, error)

// runRetime checks that the user exists, then prints a line per change and the counts on stdout.
// Any user is accepted, including a disabled or unclaimed one: it is a repair, and their media's
// times are just as wrong. A dry run (apply false) writes nothing and says so. An error, an
// interrupt or a failed item makes the command exit 1.
func runRetime(ctx context.Context, stdout io.Writer, retime retimeFunc, users []auth.UserRecord, userID string, apply bool) error {
	// Postgres emits lowercase UUIDs, and the owner ID is compared with the stored one.
	userID = strings.ToLower(userID)
	if !slices.ContainsFunc(users, func(u auth.UserRecord) bool { return u.ID == userID }) {
		return fmt.Errorf("unknown user %q (list users with `server users`)", userID)
	}
	res, err := retime(ctx, userID, func(c handlers.RetimeChange) {
		fmt.Fprintln(stdout, formatRetimeChange(c))
	})
	fmt.Fprintf(stdout, "checked=%d retimed=%d unchanged=%d not_exif=%d edited=%d mismatched=%d failed=%d\n",
		res.Checked, res.Retimed, res.Unchanged, res.NotExif, res.Edited, res.Mismatch, res.Failed)
	if !apply {
		fmt.Fprintln(stdout, "dry run: nothing written; re-run with --apply")
	}
	switch {
	case ctx.Err() != nil:
		return errors.New("interrupted; re-run retime to finish")
	case err != nil:
		return err
	case res.Failed > 0:
		return fmt.Errorf("%d item(s) failed; see the errors above", res.Failed)
	}
	return nil
}

// formatRetimeChange is one change on one line, both times in UTC:
//
//	2019-03-13T18:28:15Z -> 2019-03-13T17:28:15Z  -1h0m0s  clock_rule(Panasonic DC-TZ200 UTC+02:00)  interpolated  <id>  P1033627.JPG
//
// The zone names where the new offset came from, and for a clock rule the rule's camera and
// offset. The gps_source is the item's before the change ("-" without a location). The file name
// comes last, so a space in it cannot shift the columns before it. Text a user chose (the file name,
// the rule's camera) is quoted when it holds a control character, so it can neither break the line
// nor reach the terminal raw.
func formatRetimeChange(c handlers.RetimeChange) string {
	zone := c.Zone
	if c.Zone == ingest.ZoneClockRule && c.ClockRule != nil {
		zone = fmt.Sprintf("%s(%s %s %s)", c.Zone, printable(c.ClockRule.Make), printable(c.ClockRule.Model),
			formatUTCOffset(c.ClockRule.OffsetMin))
	}
	return fmt.Sprintf("%s -> %s  %s  %s  %s  %s  %s",
		c.Old.UTC().Format(time.RFC3339Nano), c.New.UTC().Format(time.RFC3339Nano), formatShift(c.New.Sub(c.Old)),
		zone, dash(c.GPSSource), c.MediaID, printable(c.Filename))
}

// printable returns s, or s quoted when it holds a control character.
func printable(s string) string {
	if strings.ContainsFunc(s, unicode.IsControl) {
		return strconv.Quote(s)
	}
	return s
}

// formatShift writes a shift with its sign: -1h0m0s, +3h29m59s.
func formatShift(d time.Duration) string {
	if d > 0 {
		return "+" + d.String()
	}
	return d.String()
}

// formatUTCOffset writes minutes east of UTC as UTC+02:00 or UTC-03:30.
func formatUTCOffset(minutes int) string {
	sign := "+"
	if minutes < 0 {
		sign, minutes = "-", -minutes
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, minutes/60, minutes%60)
}

// cameraZone is the server's camera zone as ingest reads and records it, with the camera rules. A
// user's setting or an upload's zone replaces its zone (ingest.CameraZone.In).
func cameraZone(cfg *config.Config) ingest.CameraZone {
	return ingest.CameraZone{
		Loc:         cfg.CameraTimezone,
		Set:         cfg.CameraTimezoneSet,
		From:        ingest.CameraZoneFromServer,
		UTCFallback: cfg.CameraTimezoneFallsBackToUTC(),
		Rules:       cfg.CameraRules,
	}
}

// logCameraTimezone states the zone EXIF times are read in and the camera rules file, and warns when
// CAMERA_TIMEZONE is unset and the server runs in UTC (a container without TZ), which is what made
// times an hour or two off.
func logCameraTimezone(cfg *config.Config) {
	slog.Info("camera rules", "file", cfg.CameraRulesFile, "makes", len(cfg.CameraRules))
	abbrev, offset := time.Now().In(cfg.CameraTimezone).Zone()
	if cfg.CameraTimezoneFallsBackToUTC() {
		slog.Warn("CAMERA_TIMEZONE is unset and the server runs in UTC, so photo times without a usable GPS timestamp or offset tag are read as UTC; set CAMERA_TIMEZONE to the zone camera clocks show, e.g. Europe/Warsaw")
		return
	}
	slog.Info("camera timezone", "zone", cfg.CameraTimezone.String(), "set", cfg.CameraTimezoneSet,
		"now", fmt.Sprintf("%s (%+.1fh)", abbrev, float64(offset)/3600))
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
	provenance *handlers.ProvenanceHandler
	checks     *handlers.ChecksHandler
	clockRules *handlers.CameraClockRuleHandler
	settings   *handlers.SettingsHandler
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
	logCameraTimezone(cfg)
	matchWindow := time.Duration(cfg.GPSMatchWindowHours) * time.Hour
	h := routeHandlers{
		media:      handlers.NewMediaHandler(pool, s3Client, matchWindow, cfg.TripGapHours, cameraZone(cfg)),
		trips:      handlers.NewTripHandler(pool, cfg.TripGapHours),
		game:       handlers.NewGameHandler(pool, s3Client),
		gpsTracks:  handlers.NewGPSTrackHandler(pool, matchWindow),
		gpsMatcher: handlers.NewGPSMatcherHandler(pool, matchWindow),
		albums:     handlers.NewAlbumHandler(pool),
		admin:      handlers.NewAdminHandler(store, cfg.PublicOrigin),
		provenance: handlers.NewProvenanceHandler(pool, s3Client, matchWindow, cameraZone(cfg)),
		checks:     handlers.NewChecksHandler(pool, matchWindow),
		clockRules: handlers.NewCameraClockRuleHandler(pool),
		settings:   handlers.NewSettingsHandler(pool, cameraZone(cfg)),
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

			// The map loads the whole library from here (megabytes of JSON). Compression stays on
			// this read-only route: responses that carry secrets (POST /api/tokens) are left alone.
			r.With(middleware.Compress(5, "application/json")).Get("/media", h.media.List)
			r.Post("/media/upload", h.media.Upload)
			r.Post("/media/known-hashes", h.media.KnownHashes)
			r.Get("/media/{id}/url", h.media.GetURL)
			r.Patch("/media/{id}/location", h.media.UpdateLocation)
			// A capture time changed by hand, like a location set by hand, is the owner's own
			// correction of their item and can be changed back, so tokens may use these.
			r.Patch("/media/{id}/taken-at", h.media.SetTakenAt)
			r.Post("/media/shift-time", h.media.ShiftTimes)
			r.Patch("/media/{id}/view-settings", h.media.UpdateViewSettings)
			r.Get("/media/{id}/keyframes", h.media.GetKeyframes)
			r.Put("/media/{id}/keyframes", h.media.ReplaceKeyframes)

			r.Get("/trips", h.trips.List)
			r.Patch("/trips/{id}", h.trips.Update)

			r.Get("/game/round", h.game.NewRound)
			r.Post("/game/guess", h.game.SubmitGuess)

			r.Post("/gps-tracks/import", h.gpsTracks.Import)
			r.Post("/gps-tracks/upload", h.gpsTracks.Upload)
			r.Post("/gps-matcher/run", h.gpsMatcher.Run)

			r.Get("/albums", h.albums.List)
			r.Post("/albums", h.albums.Create)
			r.Patch("/albums/{id}", h.albums.Update)
			r.Get("/albums/{id}/members", h.albums.ListMembers)
			r.Post("/albums/{id}/media", h.albums.AddMedia)
			r.Post("/albums/{id}/media/remove", h.albums.RemoveMedia)
			r.Get("/media/{id}/albums", h.albums.MediaAlbums)

			// A rule changes how the owner's future uploads are dated; neither a rule nor the camera
			// list is location history, so tokens may use these.
			r.Get("/camera-clock-rules", h.clockRules.List)
			r.Post("/camera-clock-rules", h.clockRules.Create)
			r.Put("/camera-clock-rules/{id}", h.clockRules.Update)
			r.Delete("/camera-clock-rules/{id}", h.clockRules.Delete)
			r.Get("/cameras", h.clockRules.Cameras)

			// The caller's own settings. The camera zone only changes how their future uploads are
			// dated, and is not location history, so tokens may use these.
			r.Get("/me/settings", h.settings.Get)
			r.Patch("/me/settings", h.settings.Update)

			r.Group(func(r chi.Router) {
				r.Use(auth.RequireSession)
				r.Delete("/media/{id}", h.media.Delete) // irreversible (S3 object removed): never via token
				r.Delete("/trips/{id}", h.trips.Delete)
				// These return the owner's location history and capture evidence: never via token.
				r.With(middleware.Compress(5, "application/json")).Get("/media/{id}/provenance", h.provenance.Get)
				r.Get("/media/{id}/provenance/file", h.provenance.File)
				// These return the owner's track files and findings drawn from their location
				// history: never via token. A large library's file list runs to hundreds of KB.
				r.With(middleware.Compress(5, "application/json")).Get("/checks", h.checks.Get)
				r.With(middleware.Compress(5, "application/json")).Get("/checks/agreement", h.checks.Agreement)
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
