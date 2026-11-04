package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x@localhost/db")
	for _, k := range []string{"PUBLIC_URL", "OIDC_ISSUER_URL", "OIDC_DISCOVERY_URL", "OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET", "OIDC_SCOPES", "SESSION_TTL_HOURS", "S3_ACCESS_KEY", "S3_SECRET_KEY", "CAMERA_TIMEZONE"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:5173", cfg.PublicURL)
	assert.Equal(t, "http://localhost:5173", cfg.PublicOrigin)
	assert.False(t, cfg.CookieSecure)
	assert.Equal(t, "http://localhost:8180/realms/geogallery", cfg.OIDCIssuerURL)
	assert.Equal(t, cfg.OIDCIssuerURL, cfg.OIDCDiscoveryURL)
	assert.Equal(t, "geogallery", cfg.OIDCClientID)
	assert.Equal(t, DevOIDCClientSecret, cfg.OIDCClientSecret)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.OIDCScopes)
	assert.Equal(t, 720, cfg.SessionTTLHours)
	assert.Same(t, time.Local, cfg.CameraTimezone, "unset, the server's own zone")
	assert.False(t, cfg.CameraTimezoneSet)
}

func TestLoad_PublicURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantOrigin string
		wantSecure bool
		wantErr    bool
	}{
		{"plain", "http://localhost:5173", "http://localhost:5173", false, false},
		{"trailing slash trimmed", "https://gallery.example.com/", "https://gallery.example.com", true, false},
		{"port kept", "https://gallery.example.com:8443", "https://gallery.example.com:8443", true, false},
		{"host lowercased", "https://Gallery.Example.COM", "https://gallery.example.com", true, false},
		{"scheme lowercased", "HTTPS://gallery.example.com", "https://gallery.example.com", true, false},
		{"default https port stripped", "https://gallery.example.com:443", "https://gallery.example.com", true, false},
		{"default http port stripped", "http://localhost:80", "http://localhost", false, false},
		{"https on port 80 kept", "https://gallery.example.com:80", "https://gallery.example.com:80", true, false},
		{"ipv6 default port stripped", "https://[::1]:443", "https://[::1]", true, false},
		{"path rejected", "https://gallery.example.com/app", "", false, true},
		{"path with slash rejected", "https://gallery.example.com/app/", "", false, true},
		{"double slash rejected", "https://gallery.example.com//", "", false, true},
		{"query rejected", "https://gallery.example.com?x=1", "", false, true},
		{"fragment rejected", "https://gallery.example.com#x", "", false, true},
		{"userinfo rejected", "https://u:p@gallery.example.com", "", false, true},
		{"no scheme rejected", "gallery.example.com", "", false, true},
		{"ftp rejected", "ftp://gallery.example.com", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("PUBLIC_URL", tt.raw)
			t.Setenv("OIDC_CLIENT_SECRET", "real-secret")
			t.Setenv("S3_ACCESS_KEY", "real-access")
			t.Setenv("S3_SECRET_KEY", "real-secret-key")
			cfg, err := Load()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrigin, cfg.PublicOrigin)
			assert.Equal(t, tt.wantOrigin, cfg.PublicURL)
			assert.Equal(t, tt.wantSecure, cfg.CookieSecure)
		})
	}
}

func TestLoad_OIDCIssuerVerbatim(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("OIDC_ISSUER_URL", "https://tenant.auth0.com/")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "https://tenant.auth0.com/", cfg.OIDCIssuerURL)
	assert.Equal(t, "https://tenant.auth0.com/", cfg.OIDCDiscoveryURL)
}

func TestLoad_RefusesDevSecretsOverHTTPS(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		secret    string
		access    string
		secretKey string
		wantErr   bool
	}{
		{"https with real secrets", "https://g.example.com", "s", "a", "k", false},
		{"https with dev oidc secret", "https://g.example.com", "", "a", "k", true},
		{"https with minioadmin access key", "https://g.example.com", "s", "", "k", true},
		{"https with minioadmin secret key", "https://g.example.com", "s", "a", "", true},
		{"http with dev secrets", "http://localhost:5173", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("PUBLIC_URL", tt.publicURL)
			t.Setenv("OIDC_CLIENT_SECRET", tt.secret)
			t.Setenv("S3_ACCESS_KEY", tt.access)
			t.Setenv("S3_SECRET_KEY", tt.secretKey)
			_, err := Load()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoad_SessionTTL(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", 720, false},
		{"24", 24, false},
		{"0", 0, true},
		{"-5", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("SESSION_TTL_HOURS", tt.raw)
			cfg, err := Load()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.SessionTTLHours)
		})
	}
}

func TestLoad_CameraTimezone(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{"IANA name", "Europe/Warsaw", "Europe/Warsaw", ""},
		{"UTC", "UTC", "UTC", ""},
		{"unknown zone fails startup", "Europe/Atlantis", "", `CAMERA_TIMEZONE "Europe/Atlantis"`},
		{"an offset is not a zone", "+02:00", "", `CAMERA_TIMEZONE "+02:00"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("CAMERA_TIMEZONE", tt.raw)
			cfg, err := Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.CameraTimezone.String())
			assert.True(t, cfg.CameraTimezoneSet)
		})
	}
}

func TestCameraTimezoneFallsBackToUTC(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	tests := []struct {
		name string
		zone *time.Location
		set  bool
		want bool
	}{
		{"unset and the server runs in UTC", time.UTC, false, true},
		{"unset and the server has a zone", warsaw, false, false},
		{"unset, UTC in winter only", london, false, false},
		{"UTC chosen explicitly", time.UTC, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{CameraTimezone: tt.zone, CameraTimezoneSet: tt.set}
			assert.Equal(t, tt.want, cfg.CameraTimezoneFallsBackToUTC())
		})
	}
}
