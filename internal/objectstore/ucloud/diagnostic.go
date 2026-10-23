package ucloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"minimax-h3-tc/internal/objectstore"
)

type storageObservation struct {
	operation, requestID string
	status               int
}
type storageTrace struct {
	mu            sync.Mutex
	last, failure storageObservation
}
type storageTransport struct {
	base  http.RoundTripper
	trace *storageTrace
}

func storageClient(original *http.Client) (*http.Client, *storageTrace) {
	copy := *original
	base := original.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	trace := &storageTrace{}
	copy.Transport = storageTransport{base: base, trace: trace}
	return &copy, trace
}

func (t storageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	operation := storageOperation(req)
	if operation != "abort" {
		value := storageObservation{operation: operation}
		if response != nil {
			value.status = response.StatusCode
			for _, key := range []string{"X-Ufile-Request-Id", "X-Ufile-RequestId", "X-Request-Id"} {
				if id := safeRequestID(response.Header.Get(key)); id != "" {
					value.requestID = id
					break
				}
			}
		}
		t.trace.mu.Lock()
		t.trace.last = value
		// 分片失败后的 abort 不能覆盖首个真实失败的步骤和状态。
		if t.trace.failure.operation == "" && (err != nil || value.status >= 400) {
			t.trace.failure = value
		}
		t.trace.mu.Unlock()
	}
	return response, err
}

func storageOperation(req *http.Request) string {
	q := req.URL.Query()
	switch {
	case req.Method == http.MethodDelete && q.Has("uploadId"):
		return "abort"
	case req.Method == http.MethodPost && q.Has("uploads"):
		return "multipart_init"
	case req.Method == http.MethodPut && q.Has("partNumber"):
		return "multipart_part"
	case req.Method == http.MethodPost && q.Has("uploadId"):
		return "multipart_complete"
	case req.Method == http.MethodHead && req.Header.Get("Authorization") != "":
		return "object_verify"
	case req.Method == http.MethodHead:
		return "public_verify"
	case req.Method == http.MethodPut:
		return "object_upload"
	default:
		return "object_request"
	}
}

func safeRequestID(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return ""
		}
	}
	return value
}

func (t *storageTrace) wrap(err error) error {
	if err == nil {
		return nil
	}
	t.mu.Lock()
	observed := t.failure
	if observed.operation == "" {
		observed = t.last
	}
	t.mu.Unlock()
	var existing *objectstore.Error
	if observed.operation == "" && errors.As(err, &existing) {
		return err
	}
	if observed.operation == "" {
		observed.operation = "local_file"
	}
	labels := map[string]string{"multipart_init": "初始化分片", "multipart_part": "上传分片", "multipart_complete": "合并分片", "object_verify": "对象校验", "public_verify": "公开读取校验", "object_upload": "上传对象", "local_file": "读取本地文件", "object_request": "对象请求"}
	message := "UCloud " + labels[observed.operation] + "失败"
	if observed.status > 0 {
		message += fmt.Sprintf("（HTTP %d）", observed.status)
	}
	failure := &objectstore.Error{Code: "ucloud_upload_failed", Message: message, Operation: observed.operation, HTTPStatus: observed.status, RequestID: observed.requestID, Reason: "request_failed", Retryable: observed.status == 0 || observed.status == 429 || observed.status >= 500, Cause: err}
	if errors.As(err, &existing) {
		failure.Code = existing.Code
		failure.Retryable = existing.Retryable
	}
	if observed.operation == "public_verify" {
		failure.Code = "ucloud_public_read_failed"
	} else if observed.status == 401 || observed.status == 403 {
		failure.Code = "ucloud_auth_failed"
	}
	if observed.status >= 300 {
		failure.Reason = "http_status"
	}
	var timeout net.Error
	switch {
	case errors.Is(err, context.Canceled):
		failure.Reason = "cancelled"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout():
		failure.Reason = "timeout"
		failure.Message += "：请求超时"
		failure.Retryable = true
	case errors.Is(err, os.ErrNotExist):
		failure.Reason = "local_file_missing"
		failure.Retryable = false
	case errors.Is(err, os.ErrPermission):
		failure.Reason = "local_file_permission"
		failure.Retryable = false
	}
	return failure
}
