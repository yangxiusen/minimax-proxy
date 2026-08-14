package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnsafeURL        = errors.New("目标 URL 不安全")
	ErrResponseTooLarge = errors.New("响应体超过限制")
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type Options struct {
	Resolver     Resolver
	Dialer       DialContextFunc
	AllowedPorts []string
}

type Guard struct {
	resolver Resolver
	dialer   DialContextFunc
	ports    map[string]struct{}
}

type Target struct {
	URL       *url.URL
	Addresses []netip.Addr
}

func New(options Options) *Guard {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	ports := map[string]struct{}{"80": {}, "443": {}}
	for _, port := range options.AllowedPorts {
		if port = strings.TrimSpace(port); port != "" {
			ports[port] = struct{}{}
		}
	}
	return &Guard{resolver: resolver, dialer: dialer, ports: ports}
}

func (g *Guard) Validate(ctx context.Context, rawURL string) (Target, error) {
	if g == nil {
		return Target{}, fmt.Errorf("%w: 校验器未配置", ErrUnsafeURL)
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Target{}, fmt.Errorf("%w: URL 格式无效", ErrUnsafeURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Target{}, fmt.Errorf("%w: 仅允许 HTTP/HTTPS", ErrUnsafeURL)
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("%w: 主机、用户信息或片段无效", ErrUnsafeURL)
	}
	port, err := normalizePort(parsed.Scheme, parsed.Port())
	if err != nil {
		return Target{}, err
	}
	if _, ok := g.ports[port]; !ok {
		return Target{}, fmt.Errorf("%w: 端口不在允许列表", ErrUnsafeURL)
	}
	addresses, err := g.lookup(ctx, parsed.Hostname())
	if err != nil {
		return Target{}, err
	}
	copyURL := *parsed
	return Target{URL: &copyURL, Addresses: addresses}, nil
}

func (g *Guard) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	normalizedHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return nil, fmt.Errorf("%w: localhost 保留域不可访问", ErrUnsafeURL)
	}
	if literal, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		literal = literal.Unmap()
		if isUnsafe(literal) {
			return nil, fmt.Errorf("%w: 地址属于受保护网络", ErrUnsafeURL)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("%w: DNS 解析失败", ErrUnsafeURL)
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if isUnsafe(address) {
			return nil, fmt.Errorf("%w: DNS 包含受保护网络地址", ErrUnsafeURL)
		}
		result = append(result, address)
	}
	return result, nil
}

// IsProtectedAddress reports whether an address is unsafe for externally
// reachable media, callback, or artifact URLs.
func IsProtectedAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsInterfaceLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	for _, prefix := range protectedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isUnsafe(address netip.Addr) bool { return IsProtectedAddress(address) }

var protectedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func (g *Guard) Client(timeout time.Duration, maxResponseBytes int64, maxRedirects int) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		DialContext:           g.dialContext,
	}
	return g.ClientWithTransport(timeout, maxResponseBytes, maxRedirects, transport)
}

func (g *Guard) ClientWithTransport(timeout time.Duration, maxResponseBytes int64, maxRedirects int, transport http.RoundTripper) *http.Client {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if maxResponseBytes <= 0 {
		maxResponseBytes = 64 << 10
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: guardedTransport{guard: g, base: transport, maxResponseBytes: maxResponseBytes},
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("重定向次数超过限制")
		}
		if _, err := g.Validate(request.Context(), request.URL.String()); err != nil {
			return fmt.Errorf("重定向目标校验失败: %w", err)
		}
		return nil
	}
	return client
}

func (g *Guard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("解析目标地址: %w", err)
	}
	if _, ok := g.ports[port]; !ok {
		return nil, fmt.Errorf("%w: 端口不在允许列表", ErrUnsafeURL)
	}
	addresses, err := g.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, candidate := range addresses {
		conn, err := g.dialer(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = errors.Join(dialErr, err)
	}
	return nil, fmt.Errorf("连接回调目标失败: %w", dialErr)
}

type guardedTransport struct {
	guard            *Guard
	base             http.RoundTripper
	maxResponseBytes int64
}

func (t guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, err := t.guard.Validate(request.Context(), request.URL.String()); err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = &boundedReadCloser{source: response.Body, remaining: t.maxResponseBytes}
	return response, nil
}

type boundedReadCloser struct {
	source    io.ReadCloser
	remaining int64
	exceeded  bool
}

func (r *boundedReadCloser) Read(buffer []byte) (int, error) {
	if r.exceeded {
		return 0, ErrResponseTooLarge
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	want := int64(len(buffer))
	if want > r.remaining+1 {
		want = r.remaining + 1
	}
	temporary := make([]byte, int(want))
	n, err := r.source.Read(temporary)
	allowed := n
	if int64(allowed) > r.remaining {
		allowed = int(r.remaining)
		r.exceeded = true
		err = ErrResponseTooLarge
	}
	copy(buffer, temporary[:allowed])
	r.remaining -= int64(allowed)
	if r.remaining == 0 && err == nil && n == allowed {
		// 下一次读取会探测是否确实还有数据，恰好等于上限的响应不会被误判。
	}
	return allowed, err
}

func (r *boundedReadCloser) Close() error { return r.source.Close() }

func normalizePort(scheme, port string) (string, error) {
	if port == "" {
		if scheme == "https" {
			return "443", nil
		}
		return "80", nil
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", fmt.Errorf("%w: 端口无效", ErrUnsafeURL)
	}
	return strconv.FormatUint(value, 10), nil
}
