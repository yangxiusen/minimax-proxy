package resultdelivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const DefaultMaxVideoBytes int64 = 2 << 30

type ResolveFunc func(context.Context, string) ([]net.IPAddr, error)

type Downloader struct {
	Client   *http.Client
	Resolve  ResolveFunc
	MaxBytes int64
}

type DownloadError struct {
	Message   string
	Retryable bool
}

func (err *DownloadError) Error() string { return err.Message }

func (d Downloader) Download(ctx context.Context, rawURL, destination string) (int64, error) {
	resolve := d.Resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupIPAddr
	}
	validate := func(ctx context.Context, target *url.URL) error {
		if target.Scheme != "https" || target.Hostname() == "" || target.User != nil {
			return errors.New("结果地址必须是无凭据的 HTTPS URL")
		}
		addresses, err := resolve(ctx, target.Hostname())
		if err != nil || len(addresses) == 0 {
			return errors.New("结果地址 DNS 解析失败")
		}
		for _, address := range addresses {
			if unsafeIP(address.IP) {
				return errors.New("结果地址指向不允许的网络")
			}
		}
		return nil
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return 0, errors.New("结果地址无效")
	}
	if err := validate(ctx, target); err != nil {
		return 0, err
	}
	client := d.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, resolved := range addresses {
				if unsafeIP(resolved.IP) {
					return nil, errors.New("结果地址拨号目标不允许")
				}
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
				if dialErr == nil {
					return connection, nil
				}
			}
			return nil, errors.New("结果地址连接失败")
		}
		client = &http.Client{Timeout: 30 * time.Minute, Transport: transport}
	}
	copyClient := *client
	previousRedirect := copyClient.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("结果下载重定向次数过多")
		}
		if err := validate(request.Context(), request.URL); err != nil {
			return err
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, err
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, &DownloadError{Message: fmt.Sprintf("结果下载 HTTP %d", response.StatusCode), Retryable: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500}
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "video/") {
		return 0, errors.New("结果下载响应不是视频")
	}
	limit := d.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxVideoBytes
	}
	if response.ContentLength > limit {
		return 0, errors.New("结果视频超过大小限制")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return written, errors.Join(copyErr, closeErr)
	}
	if written == 0 || written > limit {
		return written, errors.New("结果视频为空或超过大小限制")
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return written, errors.New("结果视频响应被截断")
	}
	return written, nil
}

func unsafeIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
