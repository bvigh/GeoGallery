// Package oidctest is an in-process OIDC issuer for tests: discovery, JWKS, an authorize
// endpoint that immediately redirects back with a code, token, userinfo and end_session.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const keyID = "test-key"

// Issuer behaviour flags may be changed between logins; they are read when the token is issued.
type Issuer struct {
	*httptest.Server

	ClientID     string
	ClientSecret string

	mu                sync.Mutex
	Sub               string
	Email             string
	Name              string
	PreferredUsername string
	OmitProfileClaims bool   // ID token carries no email/name/preferred_username
	WrongSigningKey   bool   // sign the ID token with a key not in the JWKS
	WrongNonce        bool   // echo a different nonce
	DiscoveryIssuer   string // overrides "issuer" in discovery (default: server URL)
	TokenIssuer       string // overrides the ID token "iss" (default: discovery issuer)
	UserInfoSub       string // overrides userinfo "sub" (default: Sub)
	NoEndSession      bool   // omit end_session_endpoint from discovery

	key      *rsa.PrivateKey
	wrongKey *rsa.PrivateKey
	codes    map[string]authRequest
}

type authRequest struct {
	nonce     string
	challenge string
}

func New(t *testing.T) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("oidctest: generate key: %v", err)
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("oidctest: generate key: %v", err)
	}
	iss := &Issuer{
		ClientID: "geogallery", ClientSecret: "test-secret",
		Sub: "user-sub-1", Email: "alice@example.test", Name: "Alice Test", PreferredUsername: "alice",
		key: key, wrongKey: wrongKey, codes: map[string]authRequest{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", iss.discovery)
	mux.HandleFunc("GET /jwks", iss.jwks)
	mux.HandleFunc("GET /authorize", iss.authorize)
	mux.HandleFunc("POST /token", iss.token)
	mux.HandleFunc("GET /userinfo", iss.userinfo)
	mux.HandleFunc("GET /end_session", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	iss.Server = httptest.NewServer(mux)
	t.Cleanup(iss.Close)
	return iss
}

func (i *Issuer) Set(fn func(i *Issuer)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	fn(i)
}

func (i *Issuer) issuer() string {
	if i.DiscoveryIssuer != "" {
		return i.DiscoveryIssuer
	}
	return i.URL
}

func (i *Issuer) discovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	doc := map[string]any{
		"issuer":                                i.issuer(),
		"authorization_endpoint":                i.URL + "/authorize",
		"token_endpoint":                        i.URL + "/token",
		"userinfo_endpoint":                     i.URL + "/userinfo",
		"jwks_uri":                              i.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	}
	if !i.NoEndSession {
		doc["end_session_endpoint"] = i.URL + "/end_session"
	}
	writeJSON(w, doc)
}

func (i *Issuer) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &i.key.PublicKey, KeyID: keyID, Algorithm: "RS256", Use: "sig"}}})
}

func (i *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != i.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	code := randomHex()
	i.mu.Lock()
	i.codes[code] = authRequest{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	i.mu.Unlock()
	v := redirect.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	redirect.RawQuery = v.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != i.ClientID || secret != i.ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	req, ok := i.codes[r.PostForm.Get("code")]
	delete(i.codes, r.PostForm.Get("code"))
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}

	nonce := req.nonce
	if i.WrongNonce {
		nonce = "wrong-" + nonce
	}
	iss := i.issuer()
	if i.TokenIssuer != "" {
		iss = i.TokenIssuer
	}
	now := time.Now()
	claims := map[string]any{
		"iss": iss, "sub": i.Sub, "aud": i.ClientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	if !i.OmitProfileClaims {
		claims["email"] = i.Email
		claims["name"] = i.Name
		claims["preferred_username"] = i.PreferredUsername
	}
	signKey := i.key
	if i.WrongSigningKey {
		signKey = i.wrongKey
	}
	idToken, err := sign(signKey, claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "at-" + i.Sub, "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func (i *Issuer) userinfo(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer at-"+i.Sub {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sub := i.Sub
	if i.UserInfoSub != "" {
		sub = i.UserInfoSub
	}
	writeJSON(w, map[string]any{"sub": sub, "email": i.Email, "name": i.Name, "preferred_username": i.PreferredUsername})
}

func sign(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

func randomHex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
