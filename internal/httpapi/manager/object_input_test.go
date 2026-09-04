package manager

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
)

func TestHistoricalUCloudInputGetsMetadataAndContentLink(t *testing.T) {
	const objectURL = "https://cdn.example.com/media/MiniMax-H3/inputs/request/0-deadbeef.png"
	body := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	store := &taskStoreStub{detail: domain.AdminTaskDetail{Task: domain.Task{
		TaskID: "historical-object", APIKeyID: "owner", Status: domain.StatusFailed, Model: "MiniMax-H3", Scenario: "i2va",
		Resolution: "768P", RatioRequested: "16:9", Duration: 5,
		RequestJSON: `{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"` + objectURL + `"}}]}`,
		CreatedAt:   time.Unix(2_000_000_000, 0), UpdatedAt: time.Unix(2_000_000_000, 0),
	}}}
	requests := make([]string, 0, 2)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.String())
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}
		response.Header.Set("Content-Type", "image/png")
		response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		response.ContentLength = int64(len(body))
		if request.Method == http.MethodGet {
			response.Body = io.NopCloser(bytes.NewReader(body))
		}
		return response, nil
	})}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: store,
		ObjectStorage:     &objectStorageStoreStub{config: domain.ObjectStorageConfig{Version: 1, PublicBaseURL: "https://cdn.example.com/media"}},
		InputObjectClient: client,
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	detail := serve(h, http.MethodGet, "/manager/api/tasks/historical-object", "", "", cookie, "192.0.2.10:1", false)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var payload struct {
		Request struct {
			Content []map[string]any `json:"content"`
		} `json:"request"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	item := payload.Request.Content[0]
	inputID, _ := item["input_id"].(string)
	if item["source_kind"] != "object_storage" || item["media_type"] != "image/png" || item["extension"] != ".png" ||
		item["file_name"] != "0-deadbeef.png" || item["size_bytes"] != float64(len(body)) || inputID == "" {
		t.Fatalf("historical metadata=%+v", item)
	}
	contentPath := "/manager/api/tasks/historical-object/inputs/" + inputID + "/content?download=1"
	download := serve(h, http.MethodGet, contentPath, "", "", cookie, "192.0.2.10:1", false)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), body) || !strings.Contains(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download status=%d disposition=%q body=%x", download.Code, download.Header().Get("Content-Disposition"), download.Body.Bytes())
	}
	if len(requests) != 2 || !strings.HasPrefix(requests[0], "HEAD ") || !strings.HasPrefix(requests[1], "GET ") {
		t.Fatalf("object requests=%v", requests)
	}
}

func TestExternalURLInputIsNotProbedOrDownloadable(t *testing.T) {
	store := &taskStoreStub{detail: domain.AdminTaskDetail{Task: domain.Task{
		TaskID: "external-url", APIKeyID: "owner", Status: domain.StatusFailed, Model: "MiniMax-H3", Scenario: "i2va",
		Resolution: "768P", RatioRequested: "16:9", Duration: 5,
		RequestJSON: `{"content":[{"type":"image_url","role":"reference_image","image_url":{"url":"https://third-party.example/input.png"}}]}`,
		CreatedAt:   time.Unix(2_000_000_000, 0), UpdatedAt: time.Unix(2_000_000_000, 0),
	}}}
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("third-party URL must not be requested")
		return nil, nil
	})}
	h := testHandler(Dependencies{
		Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: store,
		ObjectStorage:     &objectStorageStoreStub{config: domain.ObjectStorageConfig{Version: 1, PublicBaseURL: "https://cdn.example.com/media"}},
		InputObjectClient: client,
	})
	cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
	detail := serve(h, http.MethodGet, "/manager/api/tasks/external-url", "", "", cookie, "192.0.2.10:1", false)
	if detail.Code != http.StatusOK || calls != 0 || strings.Contains(detail.Body.String(), `"input_id"`) {
		t.Fatalf("status=%d calls=%d body=%s", detail.Code, calls, detail.Body.String())
	}
}

func TestManagedObjectURLRejectsTraversalAndLookalikes(t *testing.T) {
	const publicBaseURL = "https://cdn.example.com/media"
	invalidURLs := []string{
		"https://cdn.example.com/media/MiniMax-H3/inputs/../secret.png",
		"https://cdn.example.com/media/MiniMax-H3/inputs/%2e%2e/secret.png",
		"https://cdn.example.com/media/MiniMax-H3/inputs-evil/input.png",
		"https://other.example.com/media/MiniMax-H3/inputs/input.png",
		"https://cdn.example.com/media/MiniMax-H3/inputs/input.png?token=secret",
	}
	for _, rawURL := range invalidURLs {
		if _, ok := managedObjectURL(rawURL, publicBaseURL); ok {
			t.Errorf("managedObjectURL(%q) accepted an unsafe URL", rawURL)
		}
	}
	if _, ok := managedObjectURL("https://cdn.example.com/media/MiniMax-H3/inputs/request/input.png", publicBaseURL); !ok {
		t.Fatal("managedObjectURL rejected a valid object URL")
	}
}

func TestHistoricalObjectInputRejectsUnsafeOrMismatchedMediaFormats(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		contentType string
		objectURL   string
	}{
		{name: "html image", contentType: "image_url", objectURL: "https://cdn.example.com/media/MiniMax-H3/inputs/input.html"},
		{name: "svg image", contentType: "image_url", objectURL: "https://cdn.example.com/media/MiniMax-H3/inputs/input.svg"},
		{name: "mp4 image", contentType: "image_url", objectURL: "https://cdn.example.com/media/MiniMax-H3/inputs/input.mp4"},
		{name: "png video", contentType: "video_url", objectURL: "https://cdn.example.com/media/MiniMax-H3/inputs/input.png"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &taskStoreStub{detail: domain.AdminTaskDetail{Task: domain.Task{
				TaskID: "unsafe-history", APIKeyID: "owner", Status: domain.StatusFailed, Model: "MiniMax-H3", Scenario: "i2va",
				Resolution: "768P", RatioRequested: "16:9", Duration: 5,
				RequestJSON: `{"content":[{"type":"` + testCase.contentType + `","role":"reference_image","` + testCase.contentType + `":{"url":"` + testCase.objectURL + `"}}]}`,
				CreatedAt:   time.Unix(2_000_000_000, 0), UpdatedAt: time.Unix(2_000_000_000, 0),
			}}}
			calls := 0
			h := testHandler(Dependencies{
				Admin: config.AdminConfig{Username: "admin", Password: "secret", SessionTTL: time.Hour}, Store: store,
				ObjectStorage: &objectStorageStoreStub{config: domain.ObjectStorageConfig{Version: 1, PublicBaseURL: "https://cdn.example.com/media"}},
				InputObjectClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, nil
				})},
			})
			cookie := login(t, h, "admin", "secret", "192.0.2.10:1")
			response := serve(h, http.MethodGet, "/manager/api/tasks/unsafe-history", "", "", cookie, "192.0.2.10:1", false)
			if response.Code != http.StatusOK || calls != 0 || strings.Contains(response.Body.String(), `"input_id"`) {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}
