package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"minimax-h3-tc/internal/domain"
)

func TestProfileRoutesProvideImmediateStrictCRUD(t *testing.T) {
	service := &profileServiceStub{}
	mux := http.NewServeMux()
	RegisterProfileRoutes(mux, func(next http.Handler) http.Handler { return next }, service, "admin", nil)

	body, _ := json.Marshal(createProfileRequest{ProfileConfig: apiValidConfig()})
	created := profileRequest(mux, http.MethodPost, "/manager/api/request-profiles", body)
	if created.Code != http.StatusCreated || service.createdBy != "admin" {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}

	updatedConfig := apiValidConfig()
	updatedConfig.Generation.Steps = 9
	updateBody, _ := json.Marshal(updateProfileRequest{ProfileConfig: updatedConfig, RowVersion: 1})
	updated := profileRequest(mux, http.MethodPut, "/manager/api/request-profiles/profile-1", updateBody)
	if updated.Code != http.StatusOK || service.updatedBy != "admin" {
		t.Fatalf("status=%d body=%s", updated.Code, updated.Body.String())
	}

	deleted := profileRequest(mux, http.MethodDelete, "/manager/api/request-profiles/profile-1", []byte(`{"row_version":2}`))
	if deleted.Code != http.StatusNoContent || service.deletedVersion != 2 {
		t.Fatalf("status=%d body=%s", deleted.Code, deleted.Body.String())
	}

	legacy := profileRequest(mux, http.MethodPost, "/manager/api/request-profiles", []byte(`{"model":"MiniMax-H3","resolution":"2K"}`))
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	legacyAVSync := bytes.Replace(body, []byte(`"interpolation":{"enabled":true,"engine":"rife","scale":2}`), []byte(`"interpolation":{"enabled":true,"engine":"rife","scale":2,"av_sync_tolerance_ms":50}`), 1)
	if bytes.Equal(legacyAVSync, body) {
		t.Fatal("test fixture did not inject av_sync_tolerance_ms")
	}
	if response := profileRequest(mux, http.MethodPost, "/manager/api/request-profiles", legacyAVSync); response.Code != http.StatusBadRequest {
		t.Fatalf("legacy av sync status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProfileListRejectsVersionFilters(t *testing.T) {
	mux := http.NewServeMux()
	RegisterProfileRoutes(mux, func(next http.Handler) http.Handler { return next }, &profileServiceStub{}, "admin", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/manager/api/request-profiles?status=active", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func profileRequest(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type profileServiceStub struct {
	createdBy, updatedBy string
	deletedVersion       int64
}

func (s *profileServiceStub) Create(_ context.Context, resolution string, config domain.ProfileConfig, administrator string) (domain.ModelRequestProfile, error) {
	s.createdBy = administrator
	return domain.ModelRequestProfile{ID: "profile-1", Resolution: resolution, Config: config, RowVersion: 1}, nil
}
func (s *profileServiceStub) Update(_ context.Context, id string, version int64, config domain.ProfileConfig, administrator string) (domain.ModelRequestProfile, error) {
	s.updatedBy = administrator
	return domain.ModelRequestProfile{ID: id, Resolution: config.Resolution, Config: config, RowVersion: version + 1}, nil
}
func (s *profileServiceStub) Get(context.Context, string) (domain.ModelRequestProfile, error) {
	return domain.ModelRequestProfile{ID: "profile-1", Resolution: "2K", Config: apiValidConfig(), RowVersion: 1}, nil
}
func (s *profileServiceStub) List(context.Context) ([]domain.ModelRequestProfile, error) {
	return []domain.ModelRequestProfile{}, nil
}
func (s *profileServiceStub) Delete(_ context.Context, _ string, version int64) error {
	s.deletedVersion = version
	return nil
}

func apiValidConfig() domain.ProfileConfig {
	ratios := make(map[string]domain.RatioMapping)
	base := map[string][2]int{"adaptive": {832, 480}, "21:9": {1120, 480}, "16:9": {832, 480}, "4:3": {640, 480}, "1:1": {480, 480}, "3:4": {480, 640}, "9:16": {480, 832}}
	for ratio, size := range base {
		ratios[ratio] = domain.RatioMapping{BaseWidth: size[0], BaseHeight: size[1], TargetWidth: size[0] * 3, TargetHeight: size[1] * 3}
	}
	return domain.ProfileConfig{Resolution: "2K", Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "easycache"}, Ratios: ratios, LoRAs: []domain.LoRAProfile{}, Interpolation: domain.InterpolationProfile{Enabled: true, Engine: "rife", Scale: 2}, Restoration: domain.RestorationProfile{Enabled: true, Engine: "seedvr2", Scale: 3}}
}
