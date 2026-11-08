package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bvigh/geogallery/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Provider struct {
	oidc          *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	oauth         oauth2.Config
	issuer        string // configured issuer; users are keyed by this, not by the token's raw iss
	endSessionURL string // "" when discovery advertises no end_session_endpoint
}

// NewProvider runs OIDC discovery. It fails when the issuer is unreachable or when the
// discovered issuer differs from OIDC_ISSUER_URL, including under the discovery override.
func NewProvider(ctx context.Context, cfg *config.Config) (*Provider, error) {
	discovery := cfg.OIDCIssuerURL
	if cfg.OIDCDiscoveryURL != cfg.OIDCIssuerURL {
		// Docker/Keycloak split only: the browser sees localhost:8180, the backend reaches keycloak:8080.
		slog.Warn("oidc: discovery URL differs from issuer", "issuer", cfg.OIDCIssuerURL, "discovery", cfg.OIDCDiscoveryURL)
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.OIDCIssuerURL)
		discovery = cfg.OIDCDiscoveryURL
	}
	p, err := oidc.NewProvider(ctx, discovery)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery at %s: %w", discovery, err)
	}
	var meta struct {
		Issuer     string `json:"issuer"`
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, fmt.Errorf("oidc discovery metadata: %w", err)
	}
	if meta.Issuer != cfg.OIDCIssuerURL {
		return nil, fmt.Errorf("OIDC_ISSUER_URL does not match discovery issuer: configured=%q discovered=%q",
			cfg.OIDCIssuerURL, meta.Issuer)
	}
	return &Provider{
		oidc:     p,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  cfg.PublicOrigin + "/api/auth/callback",
			Scopes:       cfg.OIDCScopes,
		},
		issuer:        cfg.OIDCIssuerURL,
		endSessionURL: meta.EndSession,
	}, nil
}
