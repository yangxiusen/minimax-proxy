package artifact

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/store/sqlite"
	"minimax-h3-tc/internal/upstream/nodeapi"
)

var (
	ErrNotFound     = errors.New("产物不存在")
	ErrUnauthorized = errors.New("产物下载鉴权失败")
	ErrExpired      = errors.New("产物已过期")
	ErrUnavailable  = errors.New("产物节点不可用")
	ErrIntegrity    = errors.New("产物完整性校验失败")
	ErrInvalidRange = errors.New("产物 Range 无效")
	ErrBusy         = errors.New("产物下载并发已满")
)

type Store interface {
	GetArtifactAccess(context.Context, string) (sqlite.ArtifactAccess, error)
}

type MigrationStore interface {
	GetArtifact(context.Context, string) (sqlite.TaskArtifact, error)
	GetArtifactLocation(context.Context, string) (sqlite.ArtifactLocation, error)
	GetActiveArtifactLocation(context.Context, string, string) (sqlite.ArtifactLocation, error)
	CreateArtifactLocation(context.Context, sqlite.ArtifactLocation) error
	UpdateImportingArtifactLocation(context.Context, string, string, int64, string) error
	ActivatePrimaryArtifactLocation(context.Context, string, string, int64, string) error
}

type NodeStore interface {
	GetModelNode(context.Context, string) (domain.ModelNode, error)
}

type SecretOpener interface {
	Open(nonce, ciphertext []byte) (string, error)
}

type NodeClient interface {
	GetArtifact(context.Context, string, string) (nodeapi.Artifact, error)
	GetArtifactContent(context.Context, string, string, string) (*nodeapi.ArtifactContent, error)
	ImportArtifact(context.Context, string, nodeapi.ImportArtifactRequest) (nodeapi.Artifact, error)
}

type ClientFactory func(*url.URL, string, *http.Client, int64) NodeClient

type Options struct {
	SigningKey       []byte
	URLPrefix        string
	TTL              time.Duration
	Now              func() time.Time
	HTTPClient       *http.Client
	MaxJSONBody      int64
	ClientFactory    ClientFactory
	OwnerConcurrency int
	NodeConcurrency  int
}

type Service struct {
	store         Store
	nodes         NodeStore
	secrets       SecretOpener
	signingKey    []byte
	urlPrefix     string
	ttl           time.Duration
	now           func() time.Time
	httpClient    *http.Client
	maxJSONBody   int64
	clientFactory ClientFactory
	ownerLimiter  *keyedLimiter
	nodeLimiter   *keyedLimiter
	migrationMu   sync.Mutex
}

type Authorization struct {
	BearerOwner string
	Expires     int64
	Signature   string
	Method      string
}

type Content struct {
	Body          io.ReadCloser
	StatusCode    int
	ContentLength int64
	ContentRange  string
	ContentType   string
	ETag          string
	ArtifactID    string
}

type MigrationRequest struct {
	OperationID      string
	ArtifactID       string
	SourceNodeID     string
	TargetNodeID     string
	TargetLocationID string
	Filename         string
}

func NewService(store Store, nodes NodeStore, secrets SecretOpener, options Options) (*Service, error) {
	if store == nil || nodes == nil || secrets == nil {
		return nil, errors.New("产物服务依赖未配置")
	}
	if len(options.SigningKey) < 32 {
		return nil, errors.New("产物下载签名密钥至少需要 32 字节")
	}
	if options.URLPrefix == "" {
		options.URLPrefix = "/v2/files"
	}
	if options.TTL <= 0 {
		options.TTL = 15 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if options.MaxJSONBody <= 0 {
		options.MaxJSONBody = 1 << 20
	}
	if options.OwnerConcurrency <= 0 {
		options.OwnerConcurrency = 4
	}
	if options.NodeConcurrency <= 0 {
		options.NodeConcurrency = 8
	}
	if options.ClientFactory == nil {
		options.ClientFactory = func(base *url.URL, key string, client *http.Client, max int64) NodeClient {
			return nodeapi.NewClient(base, key, client, max)
		}
	}
	return &Service{
		store: store, nodes: nodes, secrets: secrets, signingKey: append([]byte(nil), options.SigningKey...),
		urlPrefix: strings.TrimSuffix(options.URLPrefix, "/"), ttl: options.TTL, now: options.Now,
		httpClient: options.HTTPClient, maxJSONBody: options.MaxJSONBody, clientFactory: options.ClientFactory,
		ownerLimiter: newKeyedLimiter(options.OwnerConcurrency), nodeLimiter: newKeyedLimiter(options.NodeConcurrency),
	}, nil
}

func (s *Service) SignURL(artifactID, ownerID string) (string, error) {
	if artifactID == "" || ownerID == "" {
		return "", ErrUnauthorized
	}
	expires := s.now().UTC().Add(s.ttl).Unix()
	signature := s.signature(http.MethodGet, artifactID, ownerID, expires)
	values := url.Values{"expires": {strconv.FormatInt(expires, 10)}, "signature": {signature}}
	return s.urlPrefix + "/" + url.PathEscape(artifactID) + "/content?" + values.Encode(), nil
}

func (s *Service) Open(ctx context.Context, requestID, artifactID, rangeHeader string, auth Authorization) (*Content, error) {
	access, err := s.store.GetArtifactAccess(ctx, artifactID)
	if errors.Is(err, sqlite.ErrArtifactNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.authorize(access.APIKeyID, artifactID, auth); err != nil {
		return nil, err
	}
	if access.TaskStatus != domain.StatusSucceeded || access.TaskDeletedAt != 0 || access.Artifact.State != "active" || access.Artifact.Kind != "final_video" {
		return nil, ErrNotFound
	}
	if access.Artifact.ExpiresAt > 0 && access.Artifact.ExpiresAt <= s.now().UTC().UnixMilli() {
		return nil, ErrExpired
	}
	releaseOwner, ok := s.ownerLimiter.acquire(access.APIKeyID)
	if !ok {
		return nil, ErrBusy
	}
	releaseNode, ok := s.nodeLimiter.acquire(access.Location.NodeID)
	if !ok {
		releaseOwner()
		return nil, ErrBusy
	}
	release := func() { releaseNode(); releaseOwner() }
	client, err := s.nodeClient(ctx, access.Location.NodeID)
	if err != nil {
		release()
		return nil, ErrUnavailable
	}
	result, err := client.GetArtifactContent(ctx, requestID, access.Location.NodeArtifactID, rangeHeader)
	if err != nil {
		release()
		var httpError *nodeapi.HTTPError
		if errors.As(err, &httpError) && (httpError.StatusCode == http.StatusNotFound || httpError.StatusCode == http.StatusGone) {
			return nil, ErrExpired
		}
		if errors.As(err, &httpError) && httpError.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, ErrInvalidRange
		}
		return nil, ErrUnavailable
	}
	if !validETag(result.ETag, access.Artifact.SHA256) || !validContentRange(result, rangeHeader, access.Artifact.SizeBytes) {
		_ = result.Body.Close()
		release()
		return nil, ErrIntegrity
	}
	return &Content{
		Body: &releaseReadCloser{ReadCloser: result.Body, release: release}, StatusCode: result.StatusCode, ContentLength: result.ContentLength,
		ContentRange: result.ContentRange, ContentType: safeContentType(result.ContentType), ETag: result.ETag,
		ArtifactID: artifactID,
	}, nil
}

func (s *Service) Migrate(ctx context.Context, requestID string, input MigrationRequest) (sqlite.ArtifactLocation, error) {
	s.migrationMu.Lock()
	defer s.migrationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return sqlite.ArtifactLocation{}, err
	}
	migrations, ok := s.store.(MigrationStore)
	if !ok {
		return sqlite.ArtifactLocation{}, errors.New("产物迁移仓储未配置")
	}
	if input.OperationID == "" || input.ArtifactID == "" || input.SourceNodeID == "" || input.TargetNodeID == "" || input.TargetLocationID == "" || input.SourceNodeID == input.TargetNodeID {
		return sqlite.ArtifactLocation{}, errors.New("产物迁移参数无效")
	}
	logical, err := migrations.GetArtifact(ctx, input.ArtifactID)
	if err != nil || logical.State != "active" || logical.SizeBytes <= 0 || logical.SHA256 == "" {
		return sqlite.ArtifactLocation{}, ErrNotFound
	}
	source, err := migrations.GetActiveArtifactLocation(ctx, input.ArtifactID, input.SourceNodeID)
	if err != nil || source.SizeBytes != logical.SizeBytes || source.SHA256 != logical.SHA256 {
		return sqlite.ArtifactLocation{}, ErrIntegrity
	}
	sourceClient, err := s.nodeClient(ctx, input.SourceNodeID)
	if err != nil {
		return sqlite.ArtifactLocation{}, ErrUnavailable
	}
	sourceMetadata, err := sourceClient.GetArtifact(ctx, requestID, source.NodeArtifactID)
	if err != nil || !artifactMatches(logical, sourceMetadata) {
		return sqlite.ArtifactLocation{}, ErrIntegrity
	}
	targetClient, err := s.nodeClient(ctx, input.TargetNodeID)
	if err != nil {
		return sqlite.ArtifactLocation{}, ErrUnavailable
	}
	location := sqlite.ArtifactLocation{ID: input.TargetLocationID, ArtifactID: input.ArtifactID, NodeID: input.TargetNodeID, NodeArtifactID: "importing:" + input.TargetLocationID, State: "importing", SizeBytes: logical.SizeBytes, SHA256: logical.SHA256}
	if existing, getErr := migrations.GetArtifactLocation(ctx, input.TargetLocationID); getErr == nil {
		if existing.ArtifactID != input.ArtifactID || existing.NodeID != input.TargetNodeID || existing.SizeBytes != logical.SizeBytes || existing.SHA256 != logical.SHA256 {
			return sqlite.ArtifactLocation{}, ErrIntegrity
		}
		location = existing
	} else if !errors.Is(getErr, sqlite.ErrArtifactNotFound) {
		return sqlite.ArtifactLocation{}, getErr
	} else if err := migrations.CreateArtifactLocation(ctx, location); err != nil {
		return sqlite.ArtifactLocation{}, err
	}
	content, err := sourceClient.GetArtifactContent(ctx, requestID, source.NodeArtifactID, "")
	if err != nil {
		return sqlite.ArtifactLocation{}, ErrUnavailable
	}
	defer content.Body.Close()
	if content.StatusCode != http.StatusOK || content.ContentLength != logical.SizeBytes || !validETag(content.ETag, logical.SHA256) {
		return sqlite.ArtifactLocation{}, ErrIntegrity
	}
	digest := sha256.New()
	counter := &countingWriter{}
	stream := io.TeeReader(io.TeeReader(content.Body, digest), counter)
	target, err := targetClient.ImportArtifact(ctx, requestID, nodeapi.ImportArtifactRequest{
		OperationID: input.OperationID, SourceArtifactID: source.NodeArtifactID,
		ExpectedSize: logical.SizeBytes, ExpectedSHA256: logical.SHA256, Kind: nodeKind(logical.Kind),
		Filename: input.Filename, Content: stream,
	})
	if err != nil {
		return sqlite.ArtifactLocation{}, ErrUnavailable
	}
	transferredSHA := fmt.Sprintf("%x", digest.Sum(nil))
	if counter.size != logical.SizeBytes || transferredSHA != logical.SHA256 || !artifactMatches(logical, target) {
		return sqlite.ArtifactLocation{}, ErrIntegrity
	}
	if target.ArtifactID == "" || location.NodeArtifactID != "" && !strings.HasPrefix(location.NodeArtifactID, "importing:") && location.NodeArtifactID != target.ArtifactID {
		return sqlite.ArtifactLocation{}, ErrIntegrity
	}
	if err := migrations.UpdateImportingArtifactLocation(ctx, location.ID, target.ArtifactID, logical.SizeBytes, logical.SHA256); err != nil {
		return sqlite.ArtifactLocation{}, err
	}
	if err := migrations.ActivatePrimaryArtifactLocation(ctx, input.ArtifactID, location.ID, logical.SizeBytes, logical.SHA256); err != nil {
		return sqlite.ArtifactLocation{}, err
	}
	location.NodeArtifactID, location.State, location.IsPrimary = target.ArtifactID, "active", true
	return location, nil
}

func (s *Service) authorize(ownerID, artifactID string, auth Authorization) error {
	if auth.BearerOwner != "" {
		if hmac.Equal([]byte(auth.BearerOwner), []byte(ownerID)) {
			return nil
		}
		return ErrUnauthorized
	}
	if auth.Method == "" {
		auth.Method = http.MethodGet
	}
	if auth.Expires <= s.now().UTC().Unix() || auth.Expires > s.now().UTC().Add(s.ttl+time.Minute).Unix() {
		return ErrUnauthorized
	}
	expected := s.signature(auth.Method, artifactID, ownerID, auth.Expires)
	if !hmac.Equal([]byte(expected), []byte(auth.Signature)) {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) signature(method, artifactID, ownerID string, expires int64) string {
	canonical := "v1\n" + method + "\n" + artifactID + "\n" + ownerID + "\n" + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) nodeClient(ctx context.Context, nodeID string) (NodeClient, error) {
	node, err := s.nodes.GetModelNode(ctx, nodeID)
	if err != nil || !node.Enabled || !node.UsesNodeAPI() {
		return nil, ErrUnavailable
	}
	_, upstream, err := config.NormalizeModelNode(node.ModelNodeInput)
	if err != nil || upstream.ServiceURL == nil {
		return nil, ErrUnavailable
	}
	key, err := s.secrets.Open(node.APIKeyNonce, node.APIKeyCiphertext)
	if err != nil {
		return nil, ErrUnavailable
	}
	client := s.clientFactory(upstream.ServiceURL, key, s.httpClient, s.maxJSONBody)
	if client == nil {
		return nil, ErrUnavailable
	}
	return client, nil
}

func validETag(etag, digest string) bool {
	if etag == "" || digest == "" {
		return false
	}
	return strings.Trim(strings.TrimPrefix(etag, "W/"), `"`) == digest
}

func validContentRange(content *nodeapi.ArtifactContent, requested string, total int64) bool {
	if requested == "" {
		return content.StatusCode == http.StatusOK && content.ContentLength == total && content.ContentRange == ""
	}
	if content.StatusCode != http.StatusPartialContent || !strings.HasPrefix(content.ContentRange, "bytes ") {
		return false
	}
	span, totalText, ok := strings.Cut(strings.TrimPrefix(content.ContentRange, "bytes "), "/")
	if !ok {
		return false
	}
	startText, endText, ok := strings.Cut(span, "-")
	if !ok {
		return false
	}
	start, startErr := strconv.ParseInt(startText, 10, 64)
	end, endErr := strconv.ParseInt(endText, 10, 64)
	declaredTotal, totalErr := strconv.ParseInt(totalText, 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || declaredTotal != total || content.ContentLength != end-start+1 {
		return false
	}
	requestedValue := strings.TrimPrefix(requested, "bytes=")
	requestedStartText, requestedEndText, ok := strings.Cut(requestedValue, "-")
	if !ok || requestedStartText == "" || strings.Contains(requestedEndText, ",") {
		return false
	}
	requestedStart, err := strconv.ParseInt(requestedStartText, 10, 64)
	if err != nil || requestedStart != start {
		return false
	}
	requestedEnd := total - 1
	if requestedEndText != "" {
		requestedEnd, err = strconv.ParseInt(requestedEndText, 10, 64)
		if err != nil {
			return false
		}
	}
	return requestedEnd == end
}

type releaseReadCloser struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *releaseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

type keyedLimiter struct {
	limit  int
	mu     sync.Mutex
	active map[string]int
}

func newKeyedLimiter(limit int) *keyedLimiter {
	return &keyedLimiter{limit: limit, active: make(map[string]int)}
}

func (l *keyedLimiter) acquire(key string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] >= l.limit {
		return nil, false
	}
	l.active[key]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.active[key]--
		if l.active[key] == 0 {
			delete(l.active, key)
		}
	}, true
}

type countingWriter struct{ size int64 }

func (w *countingWriter) Write(data []byte) (int, error) {
	w.size += int64(len(data))
	return len(data), nil
}

func artifactMatches(logical sqlite.TaskArtifact, candidate nodeapi.Artifact) bool {
	if candidate.State != "active" || candidate.SizeBytes != logical.SizeBytes || candidate.SHA256 != logical.SHA256 || nodeKind(logical.Kind) != candidate.Kind {
		return false
	}
	if candidate.Kind != "video" {
		return true
	}
	return equalJSON([]byte(logical.MediaJSON), candidate.MediaManifest)
}

func equalJSON(left, right []byte) bool {
	if len(left) == 0 || len(right) == 0 || string(right) == "null" {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return hmac.Equal(leftCanonical, rightCanonical)
}

func nodeKind(kind string) string {
	switch kind {
	case "final_video", "intermediate_video", "test_output":
		return "video"
	case "audio_source":
		return "audio"
	case "media_manifest":
		return "manifest"
	default:
		return kind
	}
}

func safeContentType(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	switch value {
	case "video/mp4", "video/webm", "video/quicktime":
		return value
	default:
		return "application/octet-stream"
	}
}

func ParseExpires(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("expires 无效")
	}
	return parsed, nil
}
