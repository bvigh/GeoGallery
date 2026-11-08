package auth

import (
	"context"
	"testing"

	"github.com/bvigh/geogallery/internal/auth/oidctest"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerConfig(iss *oidctest.Issuer) *config.Config {
	return &config.Config{
		PublicOrigin:     "http://localhost:5173",
		OIDCIssuerURL:    iss.URL,
		OIDCDiscoveryURL: iss.URL,
		OIDCClientID:     iss.ClientID,
		OIDCClientSecret: iss.ClientSecret,
		OIDCScopes:       []string{"openid", "profile", "email"},
		SessionTTLHours:  720,
	}
}

func TestNewProvider(t *testing.T) {
	const external = "http://localhost:8180/realms/geogallery"
	tests := []struct {
		name            string
		discoveryIssuer func(url string) string
		configIssuer    func(url string) string // OIDCIssuerURL; discovery URL is always the server
		noEndSession    bool
		wantErr         string
		wantEndSession  bool
	}{
		{
			name:           "matching issuer",
			wantEndSession: true,
		},
		{
			name:            "discovered issuer has trailing slash, configured does not",
			discoveryIssuer: func(u string) string { return u + "/" },
			wantErr:         "issuer",
		},
		{
			name:            "configured issuer has trailing slash, discovered does not",
			configIssuer:    func(u string) string { return u + "/" },
			discoveryIssuer: func(u string) string { return u },
			wantErr:         "issuer",
		},
		{
			name:            "discovery override with matching external issuer",
			discoveryIssuer: func(string) string { return external },
			configIssuer:    func(string) string { return external },
			wantEndSession:  true,
		},
		{
			name:            "discovery override with mismatched discovered issuer",
			discoveryIssuer: func(u string) string { return u },
			configIssuer:    func(string) string { return external },
			wantErr:         "OIDC_ISSUER_URL does not match discovery issuer",
		},
		{
			name:         "no end_session endpoint",
			noEndSession: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iss := oidctest.New(t)
			iss.Set(func(i *oidctest.Issuer) {
				if tt.discoveryIssuer != nil {
					i.DiscoveryIssuer = tt.discoveryIssuer(i.URL)
				}
				i.NoEndSession = tt.noEndSession
			})
			cfg := providerConfig(iss)
			if tt.configIssuer != nil {
				cfg.OIDCIssuerURL = tt.configIssuer(iss.URL)
			}
			p, err := NewProvider(context.Background(), cfg)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, cfg.OIDCIssuerURL, p.issuer)
			assert.Equal(t, "http://localhost:5173/api/auth/callback", p.oauth.RedirectURL)
			if tt.wantEndSession {
				assert.Equal(t, iss.URL+"/end_session", p.endSessionURL)
			} else {
				assert.Empty(t, p.endSessionURL)
			}
		})
	}
}

func TestNewProvider_Unreachable(t *testing.T) {
	cfg := &config.Config{OIDCIssuerURL: "http://127.0.0.1:1/realms/x", OIDCDiscoveryURL: "http://127.0.0.1:1/realms/x", OIDCClientID: "c"}
	_, err := NewProvider(context.Background(), cfg)
	assert.Error(t, err)
}
