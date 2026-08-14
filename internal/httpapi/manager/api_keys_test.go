package manager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/authkey"
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

type apiKeyServiceStub struct {
	items   []domain.ExternalAPIKey
	created authkey.CreatedExternalAPIKey
	updated domain.ExternalAPIKey
	err     error
	deleted bool
}

func (s *apiKeyServiceStub) List(context.Context) ([]domain.ExternalAPIKey, error) {
	return s.items, s.err
}
func (s *apiKeyServiceStub) Create(context.Context, string) (authkey.CreatedExternalAPIKey, error) {
	return s.created, s.err
}
func (s *apiKeyServiceStub) Update(context.Context, string, int64, domain.ExternalAPIKeyUpdate) (domain.ExternalAPIKey, error) {
	return s.updated, s.err
}
func (s *apiKeyServiceStub) Delete(context.Context, string, int64) error {
	s.deleted = s.err == nil
	return s.err
}

func apiKeyManagerHandler(t *testing.T, service APIKeyService) (http.Handler, string) {
	t.Helper()
	h := NewHandler(Dependencies{Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, APIKeyService: service})
	login := serve(h, http.MethodPost, "/manager/api/session", `{"username":"admin","password":"secret"}`, "application/json", "", "192.0.2.70:1", false)
	if login.Code != http.StatusNoContent {
		t.Fatalf("login=%d %s", login.Code, login.Body.String())
	}
	return h, login.Result().Cookies()[0].Value
}

func TestAPIKeyRoutesRequireSessionAndNeverListSecrets(t *testing.T) {
	item := domain.ExternalAPIKey{ID: "key_1", Name: "生产", Key: "mmx_full_key", KeyPrefix: "mmx_ab12", KeySuffix: "89ef", Enabled: true, Version: 2, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(2000)}
	service := &apiKeyServiceStub{items: []domain.ExternalAPIKey{item}}
	h, cookie := apiKeyManagerHandler(t, service)
	if got := serve(h, http.MethodGet, "/manager/api/api-keys", "", "", "", "192.0.2.71:1", false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", got.Code)
	}
	got := serve(h, http.MethodGet, "/manager/api/api-keys", "", "", cookie, "192.0.2.71:1", false)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"key":"mmx_full_key"`) || !strings.Contains(got.Body.String(), `"masked_key":"mmx_ab12...89ef"`) || strings.Contains(got.Body.String(), "key_digest") {
		t.Fatalf("list=%d %s", got.Code, got.Body.String())
	}
}

func TestAPIKeyCreateReturnsSecretOnlyOnCreate(t *testing.T) {
	created := authkey.CreatedExternalAPIKey{ExternalAPIKey: domain.ExternalAPIKey{ID: "key_1", Name: "生产", KeyPrefix: "mmx_ab12", KeySuffix: "89ef", Enabled: true, Version: 1}, Key: "mmx_abcdef"}
	h, cookie := apiKeyManagerHandler(t, &apiKeyServiceStub{created: created})
	got := serve(h, http.MethodPost, "/manager/api/api-keys", `{"name":"生产"}`, "application/json", cookie, "192.0.2.72:1", false)
	if got.Code != http.StatusCreated || got.Header().Get("Location") != "/manager/api/api-keys/key_1" || !strings.Contains(got.Body.String(), `"key":"mmx_abcdef"`) {
		t.Fatalf("create=%d %s", got.Code, got.Body.String())
	}
}

func TestAPIKeyRoutesUseStrictJSONAndStableErrors(t *testing.T) {
	service := &apiKeyServiceStub{err: domain.ErrAPIKeyNameConflict}
	h, cookie := apiKeyManagerHandler(t, service)
	bad := serve(h, http.MethodPost, "/manager/api/api-keys", `{"name":"a","name":"b"}`, "application/json", cookie, "192.0.2.73:1", false)
	assertManagerError(t, bad, http.StatusBadRequest, "bad_request_error")
	conflict := serve(h, http.MethodPost, "/manager/api/api-keys", `{"name":"a"}`, "application/json", cookie, "192.0.2.73:1", false)
	assertManagerError(t, conflict, http.StatusConflict, "api_key_name_conflict")
	service.err = errors.Join(authkey.ErrCacheRefresh, errors.New("db private detail"))
	unavailable := serve(h, http.MethodPut, "/manager/api/api-keys/key_1", `{"name":"a","enabled":false,"version":1}`, "application/json", cookie, "192.0.2.73:1", false)
	assertManagerError(t, unavailable, http.StatusServiceUnavailable, "cache_refresh_failed")
	service.err = nil
	deleted := serve(h, http.MethodDelete, "/manager/api/api-keys/key_1?version=1", "", "", cookie, "192.0.2.73:1", false)
	if deleted.Code != http.StatusNoContent || !service.deleted {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAPIKeyRequestBodyIsLimitedToFourKiB(t *testing.T) {
	h, cookie := apiKeyManagerHandler(t, &apiKeyServiceStub{})
	body := `{"name":"` + strings.Repeat("a", 5000) + `"}`
	response := serve(h, http.MethodPost, "/manager/api/api-keys", body, "application/json", cookie, "192.0.2.74:1", false)
	assertManagerError(t, response, http.StatusBadRequest, "bad_request_error")
}

func TestAPIKeyUpdateRequiresEveryFieldAndWritesOneJSONError(t *testing.T) {
	h, cookie := apiKeyManagerHandler(t, &apiKeyServiceStub{})
	missingEnabled := serve(h, http.MethodPut, "/manager/api/api-keys/key_1", `{"name":"a","version":1}`, "application/json", cookie, "192.0.2.75:1", false)
	assertManagerError(t, missingEnabled, http.StatusBadRequest, "bad_request_error")
	badJSON := serve(h, http.MethodPut, "/manager/api/api-keys/key_1", `{"name":"a","name":"b"}`, "application/json", cookie, "192.0.2.75:1", false)
	assertManagerError(t, badJSON, http.StatusBadRequest, "bad_request_error")
	if strings.Count(badJSON.Body.String(), `{"error"`) != 1 {
		t.Fatalf("error response was written more than once: %q", badJSON.Body.String())
	}
}
