package v2

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/config"
)

type fileServiceStub struct {
	auth        artifactservice.Authorization
	rangeHeader string
	content     *artifactservice.Content
	err         error
}

func (s *fileServiceStub) Open(_ context.Context, _ string, _ string, rangeHeader string, auth artifactservice.Authorization) (*artifactservice.Content, error) {
	s.auth, s.rangeHeader = auth, rangeHeader
	return s.content, s.err
}

func TestFilesHandlerSupportsOwnerBearerAndRangeWithoutBufferingHeaders(t *testing.T) {
	service := &fileServiceStub{content: &artifactservice.Content{Body: io.NopCloser(strings.NewReader("2345")), StatusCode: http.StatusPartialContent, ContentLength: 4, ContentRange: "bytes 2-5/10", ContentType: "video/mp4", ETag: `"digest"`, ArtifactID: "artifact-1"}}
	handler := NewFilesHandler(FilesDependencies{Service: service, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "external-secret", Enabled: true}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	request := httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content", nil)
	request.Header.Set("Authorization", "Bearer external-secret")
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "2345" || service.auth.BearerOwner != "owner-a" || service.rangeHeader != "bytes=2-5" {
		t.Fatalf("response=%d %q auth=%+v range=%q", response.Code, response.Body.String(), service.auth, service.rangeHeader)
	}
	if response.Header().Get("Content-Range") != "bytes 2-5/10" || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers=%v", response.Header())
	}
}

func TestFilesHandlerAcceptsSignatureAndHidesInternalErrors(t *testing.T) {
	service := &fileServiceStub{err: artifactservice.ErrUnavailable}
	handler := NewFilesHandler(FilesDependencies{Service: service, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	request := httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content?expires=2000000000&signature=signed", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || service.auth.Signature != "signed" || strings.Contains(response.Body.String(), "node") {
		t.Fatalf("response=%d %s auth=%+v", response.Code, response.Body.String(), service.auth)
	}
}

func TestFilesHandlerRejectsInvalidBearerWithoutSignatureFallback(t *testing.T) {
	service := &fileServiceStub{}
	handler := NewFilesHandler(FilesDependencies{Service: service, APIKeys: []config.APIKeyConfig{{ID: "owner-a", Key: "right", Enabled: true}}})
	request := httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content?expires=2000000000&signature=signed", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || service.auth.Signature != "" {
		t.Fatalf("response=%d auth=%+v", response.Code, service.auth)
	}
}

func TestFilesHandlerPreservesInvalidRangeStatus(t *testing.T) {
	service := &fileServiceStub{err: artifactservice.ErrInvalidRange}
	handler := NewFilesHandler(FilesDependencies{Service: service})
	request := httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content?expires=2000000000&signature=signed", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFilesHandlerUsesLiveBearerAuthenticator(t *testing.T) {
	authenticator := &mutableBearerAuthenticator{owner: "owner-a", token: "external-secret"}
	service := &fileServiceStub{content: &artifactservice.Content{Body: io.NopCloser(strings.NewReader("ok")), StatusCode: http.StatusOK, ContentLength: 2}}
	handler := NewFilesHandler(FilesDependencies{Service: service, Authenticator: authenticator})

	request := httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content", nil)
	request.Header.Set("Authorization", "Bearer external-secret")
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusOK {
		t.Fatalf("enabled key status=%d body=%s", first.Code, first.Body.String())
	}

	authenticator.owner = ""
	request = httptest.NewRequest(http.MethodGet, "/v2/files/artifact-1/content", nil)
	request.Header.Set("Authorization", "Bearer external-secret")
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key status=%d body=%s", second.Code, second.Body.String())
	}
}
