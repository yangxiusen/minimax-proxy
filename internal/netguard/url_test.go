package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type resolverStub struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	calls   map[string]int
}

func (r *resolverStub) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	answers := r.answers[host]
	if len(answers) == 0 {
		return nil, errors.New("host not found")
	}
	index := r.calls[host]
	r.calls[host]++
	if index >= len(answers) {
		index = len(answers) - 1
	}
	return append([]netip.Addr(nil), answers[index]...), nil
}

func TestValidateRejectsUnsafeTargets(t *testing.T) {
	resolver := &resolverStub{answers: map[string][][]netip.Addr{
		"loop.example":    {{netip.MustParseAddr("127.0.0.1")}},
		"private.example": {{netip.MustParseAddr("10.1.2.3")}},
		"link.example":    {{netip.MustParseAddr("169.254.169.254")}},
		"v6.example":      {{netip.MustParseAddr("fd00::1234")}},
		"public.example":  {{netip.MustParseAddr("8.8.8.8")}},
	}}
	guard := New(Options{Resolver: resolver})
	tests := []struct {
		name string
		raw  string
	}{
		{name: "scheme", raw: "file:///tmp/video"},
		{name: "localhost name", raw: "https://localhost/hook"},
		{name: "userinfo", raw: "https://user:pass@public.example/hook"},
		{name: "loopback", raw: "https://loop.example/hook"},
		{name: "private", raw: "https://private.example/hook"},
		{name: "metadata", raw: "http://link.example/latest/meta-data"},
		{name: "ipv6 private", raw: "https://v6.example/hook"},
		{name: "unsafe port", raw: "https://public.example:8188/hook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := guard.Validate(context.Background(), tt.raw); err == nil {
				t.Fatalf("Validate(%q) expected error", tt.raw)
			}
		})
	}
	if _, err := guard.Validate(context.Background(), "https://public.example/hook"); err != nil {
		t.Fatalf("public target rejected: %v", err)
	}
}

func TestClientRejectsDNSRebindingAtDialTime(t *testing.T) {
	resolver := &resolverStub{answers: map[string][][]netip.Addr{
		"callback.example": {
			{netip.MustParseAddr("8.8.8.8")},
			{netip.MustParseAddr("127.0.0.1")},
		},
	}}
	dials := 0
	guard := New(Options{
		Resolver: resolver,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("must not dial")
		},
	})
	client := guard.Client(time.Second, 1024, 2)
	request, err := http.NewRequest(http.MethodPost, "https://callback.example/hook", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("Do() error = %v", err)
	}
	if dials != 0 {
		t.Fatalf("unsafe rebound address reached dialer: %d", dials)
	}
}

func TestClientValidatesEveryRedirectHop(t *testing.T) {
	unsafe := "http://127.0.0.1/private"
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{unsafe}},
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	resolver := &resolverStub{answers: map[string][][]netip.Addr{
		"public.example": {{netip.MustParseAddr("8.8.8.8")}},
	}}
	guard := New(Options{Resolver: resolver})
	client := guard.ClientWithTransport(time.Second, 1024, 2, transport)
	_, err := client.Get("https://public.example/start")
	if err == nil || !strings.Contains(err.Error(), "重定向") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestClientBoundsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 65)))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	host, port, _ := net.SplitHostPort(endpoint.Host)
	resolver := &resolverStub{answers: map[string][][]netip.Addr{
		"public.test": {{netip.MustParseAddr("8.8.8.8")}},
	}}
	guard := New(Options{
		Resolver:     resolver,
		AllowedPorts: []string{port},
		Dialer: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	})
	client := guard.Client(time.Second, 64, 0)
	response, err := client.Get("http://public.test:" + port)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := make([]byte, 128)
	n, readErr := response.Body.Read(data)
	if n > 64 || !errors.Is(readErr, ErrResponseTooLarge) {
		t.Fatalf("Read() = %d, %v", n, readErr)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
