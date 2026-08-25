package resultdelivery

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestDownloaderRejectsPrivateAddressBeforeRequest(t *testing.T) {
	called := false
	downloader := Downloader{
		Resolve: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })},
	}
	if _, err := downloader.Download(context.Background(), "https://origin.example/video.mp4", t.TempDir()+"/video.mp4"); err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestDownloaderValidatesVideoLengthAndType(t *testing.T) {
	resolve := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	}
	for _, test := range []struct {
		name, contentType, body string
		length                  int64
	}{
		{name: "not video", contentType: "text/html", body: "hello", length: 5},
		{name: "truncated", contentType: "video/mp4", body: "short", length: 20},
		{name: "too large", contentType: "video/mp4", body: "123456", length: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{test.contentType}}, ContentLength: test.length, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			downloader := Downloader{Resolve: resolve, Client: client, MaxBytes: 5}
			if _, err := downloader.Download(context.Background(), "https://origin.example/video.mp4", t.TempDir()+"/video.mp4"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDownloaderStreamsValidVideo(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"video/mp4"}}, ContentLength: 5, Body: io.NopCloser(strings.NewReader("video"))}, nil
	})}
	downloader := Downloader{Resolve: func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	}, Client: client}
	if size, err := downloader.Download(context.Background(), "https://origin.example/video.mp4", t.TempDir()+"/video.mp4"); err != nil || size != 5 {
		t.Fatalf("size=%d err=%v", size, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
