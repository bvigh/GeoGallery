package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
)

const (
	DevOIDCClientSecret = "geogallery-dev-secret"
	devS3Credential     = "minioadmin"
)

type Config struct {
	DatabaseURL         string
	S3Endpoint          string
	S3AccessKey         string
	S3SecretKey         string
	S3Bucket            string
	S3Region            string
	ServerPort          string
	IngestWatchDir      string
	GPSMatchWindowHours int
	TripGapHours        int
	LogLevel            string
	LogFormat           string

	// CameraTimezone is the zone camera clocks are assumed to show: a photo's EXIF time carries no
	// usable zone, so ingest reads it here unless the photo's GPS clock or offset tag says otherwise. From
	// CAMERA_TIMEZONE (an IANA name); unset, the server's own zone (TZ or /etc/localtime).
	CameraTimezone    *time.Location
	CameraTimezoneSet bool // CAMERA_TIMEZONE was set

	// CameraRules says, per camera make, which of a file's own clocks ingest must not trust. From the
	// file CAMERA_RULES_FILE names, by default ./camera-rules.json beside the binary. A missing or
	// invalid file fails startup, so nothing is ever ingested with the rules silently gone.
	CameraRulesFile string
	CameraRules     ingest.CameraRules

	PublicURL        string // normalised to scheme://host[:port]
	PublicOrigin     string // CSRF trusted origin, redirect_uri and post-logout URI base
	CookieSecure     bool   // PublicURL scheme is https
	OIDCIssuerURL    string // used verbatim: must match the discovery issuer byte for byte
	OIDCDiscoveryURL string // only differs from OIDCIssuerURL for the Docker/Keycloak split
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScopes       []string
	SessionTTLHours  int
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		S3Endpoint:          getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3AccessKey:         getEnv("S3_ACCESS_KEY", devS3Credential),
		S3SecretKey:         getEnv("S3_SECRET_KEY", devS3Credential),
		S3Bucket:            getEnv("S3_BUCKET", "geogallery"),
		S3Region:            getEnv("S3_REGION", "us-east-1"),
		ServerPort:          getEnv("SERVER_PORT", "8080"),
		IngestWatchDir:      getEnv("INGEST_WATCH_DIR", "./ingest"),
		GPSMatchWindowHours: getEnvInt("GPS_MATCH_WINDOW_HOURS", 1),
		TripGapHours:        getEnvInt("TRIP_GAP_HOURS", 24),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
		LogFormat:           getEnv("LOG_FORMAT", "text"),
		OIDCIssuerURL:       getEnv("OIDC_ISSUER_URL", "http://localhost:8180/realms/geogallery"),
		OIDCClientID:        getEnv("OIDC_CLIENT_ID", "geogallery"),
		OIDCClientSecret:    getEnv("OIDC_CLIENT_SECRET", DevOIDCClientSecret),
		OIDCScopes:          strings.Fields(getEnv("OIDC_SCOPES", "openid profile email")),
		SessionTTLHours:     getEnvInt("SESSION_TTL_HOURS", 720),
	}
	cfg.OIDCDiscoveryURL = getEnv("OIDC_DISCOVERY_URL", cfg.OIDCIssuerURL)

	cfg.CameraTimezone = time.Local
	if name := os.Getenv("CAMERA_TIMEZONE"); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, fmt.Errorf("CAMERA_TIMEZONE %q: %w", name, err)
		}
		cfg.CameraTimezone, cfg.CameraTimezoneSet = loc, true
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	cfg.CameraRulesFile = getEnv("CAMERA_RULES_FILE", "./camera-rules.json")
	rules, err := ingest.LoadCameraRules(cfg.CameraRulesFile)
	if err != nil {
		return nil, fmt.Errorf("CAMERA_RULES_FILE %q: %w", cfg.CameraRulesFile, err)
	}
	cfg.CameraRules = rules

	origin, err := normalisePublicURL(getEnv("PUBLIC_URL", "http://localhost:5173"))
	if err != nil {
		return nil, err
	}
	cfg.PublicURL = origin
	cfg.PublicOrigin = origin
	cfg.CookieSecure = strings.HasPrefix(origin, "https://")

	if cfg.SessionTTLHours <= 0 {
		return nil, fmt.Errorf("SESSION_TTL_HOURS must be positive, got %d", cfg.SessionTTLHours)
	}

	if cfg.CookieSecure {
		if !cfg.CameraTimezoneSet {
			return nil, errors.New("refusing to start over https without CAMERA_TIMEZONE: set it to the zone your camera clocks show, e.g. Europe/Warsaw; photo times must not depend on the server's zone")
		}
		if cfg.OIDCClientSecret == DevOIDCClientSecret {
			return nil, errors.New("refusing to start over https with the dev OIDC_CLIENT_SECRET")
		}
		if cfg.S3AccessKey == devS3Credential || cfg.S3SecretKey == devS3Credential {
			return nil, errors.New("refusing to start over https with the default minioadmin S3 credentials")
		}
	}

	return cfg, nil
}

// CameraTimezoneFallsBackToUTC reports whether CAMERA_TIMEZONE is unset and the server's own zone
// is UTC all year, as in a container without TZ: photo times would then be read as UTC, which is
// rarely what a camera clock shows.
func (c *Config) CameraTimezoneFallsBackToUTC() bool {
	if c.CameraTimezoneSet {
		return false
	}
	year := time.Now().Year()
	for _, month := range []time.Month{time.January, time.July} {
		if _, offset := time.Date(year, month, 1, 12, 0, 0, 0, c.CameraTimezone).Zone(); offset != 0 {
			return false
		}
	}
	return true
}

// normalisePublicURL trims one trailing slash and rejects anything beyond scheme://host[:port]:
// AddTrustedOrigin rejects paths, and a leftover slash would yield "//api/auth/callback".
func normalisePublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("PUBLIC_URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("PUBLIC_URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("PUBLIC_URL %q: host is required", raw)
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("PUBLIC_URL %q: must be scheme://host[:port] with no path, query or fragment", raw)
	}
	// Browsers send Origin with a lowercase host and no default port, and CrossOriginProtection
	// compares trusted origins as exact strings.
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		host = strings.TrimSuffix(host, ":"+u.Port())
	}
	return u.Scheme + "://" + host, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
