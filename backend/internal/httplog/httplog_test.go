package httplog

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew_LogsPathNeverQuery(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		handler    http.HandlerFunc
		wantPath   string
		wantStatus string
		secrets    []string
	}{
		{
			name:       "invite token in query",
			target:     "/api/auth/login?invite=SECRETINVITE&return_to=%2Ftrips",
			handler:    func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/x", http.StatusFound) },
			wantPath:   "path=/api/auth/login",
			wantStatus: "status=302",
			secrets:    []string{"SECRETINVITE", "invite=", "return_to", "?"},
		},
		{
			name:       "oidc code and state",
			target:     "/api/auth/callback?code=SECRETCODE&state=SECRETSTATE",
			handler:    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			wantPath:   "path=/api/auth/callback",
			wantStatus: "status=400",
			secrets:    []string{"SECRETCODE", "SECRETSTATE"},
		},
		{
			name:       "implicit 200",
			target:     "/api/health",
			handler:    func(w http.ResponseWriter, r *http.Request) {},
			wantPath:   "path=/api/health",
			wantStatus: "status=200",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Header.Set("Authorization", "Bearer gg_pat_SECRETHEADER")
			New(logger)(tt.handler).ServeHTTP(httptest.NewRecorder(), req)

			line := buf.String()
			assert.Contains(t, line, "method=GET")
			assert.Contains(t, line, tt.wantPath)
			assert.Contains(t, line, tt.wantStatus)
			assert.Contains(t, line, "duration=")
			assert.NotContains(t, line, "SECRETHEADER")
			for _, s := range tt.secrets {
				assert.NotContains(t, line, s)
			}
		})
	}
}
