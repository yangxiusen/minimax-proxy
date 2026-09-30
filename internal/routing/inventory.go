package routing

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/tk2sd"
)

const CatalogRefreshInterval = 5 * time.Minute
const CatalogValidity = 30 * time.Minute

var ErrDiscoveryFailed = errors.New("model_discovery_failed")
var ErrDiscoveryUnsupported = errors.New("model_discovery_unsupported")

type InventoryStore interface {
	ListModelNodes(context.Context) ([]domain.ModelNode, error)
	GetModelNode(context.Context, string) (domain.ModelNode, error)
	GetModelCatalog(context.Context, string) (domain.ModelCatalog, error)
	RefreshModelCatalog(context.Context, string, int64, int64, []domain.NodeModel, string) (domain.ModelCatalog, error)
}
type InventorySecrets interface {
	Open([]byte, []byte) (string, error)
}
type Inventory struct {
	Store      InventoryStore
	Secrets    InventorySecrets
	HTTPClient *http.Client
	Now        func() time.Time
	Wake       func()
	mu         sync.Mutex
	flights    map[inventoryKey]*inventoryFlight
}
type inventoryKey struct {
	id                           string
	nodeVersion, catalogRevision int64
}
type inventoryFlight struct {
	done    chan struct{}
	catalog domain.ModelCatalog
	err     error
}

func (i *Inventory) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

func (i *Inventory) Preview(ctx context.Context, input domain.ModelNodeInput, apiKey string) (domain.ModelCatalog, error) {
	if input.ProtocolVersion != domain.ProtocolTK2SD {
		return domain.ModelCatalog{}, ErrDiscoveryUnsupported
	}
	if input.ID == "" {
		input.ID = "preview"
	}
	if input.PollInterval == 0 {
		input.PollInterval = time.Second
	}
	if input.MaxConcurrency == 0 {
		input.MaxConcurrency = 1
	}
	normalized, upstream, err := config.NormalizeModelNode(input)
	if err != nil {
		return domain.ModelCatalog{}, ErrDiscoveryFailed
	}
	ctx, cancel := context.WithTimeout(ctx, normalized.RequestTimeout)
	defer cancel()
	client := http.Client{Timeout: normalized.RequestTimeout}
	if i.HTTPClient != nil {
		client = *i.HTTPClient
		client.Timeout = normalized.RequestTimeout
	}
	rows, err := tk2sd.NewClient(upstream.ServiceURL, apiKey, &client, 1<<20).Models(ctx)
	if err != nil {
		return domain.ModelCatalog{}, ErrDiscoveryFailed
	}
	grouped := map[string]*domain.NodeModel{}
	for _, row := range rows {
		if !domain.ValidModelID(row.ID) || len(row.Durations) == 0 {
			return domain.ModelCatalog{}, ErrDiscoveryFailed
		}
		scenario, roles := "", []string(nil)
		switch row.Mode {
		case "text_to_video":
			scenario = "t2va"
		case "image_to_video":
			scenario = "i2va"
			roles = []string{"first_frame"}
		case "reference_to_video":
			scenario = "r2va"
			roles = []string{"reference_image", "reference_video", "reference_audio"}
		default:
			return domain.ModelCatalog{}, ErrDiscoveryFailed
		}
		durations := slices.Clone(row.Durations)
		slices.Sort(durations)
		for index, d := range durations {
			if d < 4 || d > 15 || (index > 0 && d == durations[index-1]) {
				return domain.ModelCatalog{}, ErrDiscoveryFailed
			}
		}
		model := grouped[row.ID]
		if model == nil {
			model = &domain.NodeModel{ModelID: row.ID, Enabled: true, Present: true, Verified: true, Capabilities: domain.ModelCapability{SchemaVersion: 1, InputSources: []string{"http", "https", "data", "proxy-input"}, ResolutionPolicy: "ignored", MaxMedia: 12}}
			grouped[row.ID] = model
		}
		model.Capabilities.Modes = append(model.Capabilities.Modes, domain.ModeCapability{Scenario: scenario, Durations: durations, Roles: roles})
	}
	if len(grouped) > 256 {
		return domain.ModelCatalog{}, ErrDiscoveryFailed
	}
	now := i.now().Unix()
	catalog := domain.ModelCatalog{NodeID: input.ID, Source: "discovered", Status: "ready", LastAttemptAt: now, LastSuccessAt: now, ValidUntil: now + int64(CatalogValidity/time.Second), Items: []domain.NodeModel{}}
	for _, model := range grouped {
		sort.Slice(model.Capabilities.Modes, func(a, b int) bool {
			return model.Capabilities.Modes[a].Scenario < model.Capabilities.Modes[b].Scenario
		})
		catalog.Items = append(catalog.Items, *model)
	}
	sort.Slice(catalog.Items, func(a, b int) bool { return catalog.Items[a].ModelID < catalog.Items[b].ModelID })
	if len(catalog.Items) == 0 {
		catalog.Status = "empty"
	}
	return catalog, nil
}

func (i *Inventory) Refresh(ctx context.Context, nodeID string, nodeVersion, catalogRevision int64) (domain.ModelCatalog, error) {
	if i.Store == nil {
		return domain.ModelCatalog{}, ErrDiscoveryFailed
	}
	key := inventoryKey{nodeID, nodeVersion, catalogRevision}
	i.mu.Lock()
	if i.flights == nil {
		i.flights = make(map[inventoryKey]*inventoryFlight)
	}
	if call := i.flights[key]; call != nil {
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.ModelCatalog{}, ctx.Err()
		case <-call.done:
			return cloneCatalog(call.catalog), call.err
		}
	}
	call := &inventoryFlight{done: make(chan struct{})}
	i.flights[key] = call
	i.mu.Unlock()
	call.catalog, call.err = i.refresh(ctx, key)
	i.mu.Lock()
	delete(i.flights, key)
	close(call.done)
	i.mu.Unlock()
	return cloneCatalog(call.catalog), call.err
}

func (i *Inventory) refresh(ctx context.Context, key inventoryKey) (domain.ModelCatalog, error) {
	node, err := i.Store.GetModelNode(ctx, key.id)
	if err != nil {
		return domain.ModelCatalog{}, err
	}
	old, err := i.Store.GetModelCatalog(ctx, key.id)
	if err != nil {
		return domain.ModelCatalog{}, err
	}
	if node.Version != key.nodeVersion {
		return domain.ModelCatalog{}, domain.ErrNodeVersionConflict
	}
	if old.Revision != key.catalogRevision {
		return domain.ModelCatalog{}, domain.ErrCatalogConflict
	}
	if node.ProtocolVersion != domain.ProtocolTK2SD || old.Source != "discovered" {
		return domain.ModelCatalog{}, ErrDiscoveryUnsupported
	}
	var catalog domain.ModelCatalog
	if i.Secrets == nil {
		err = ErrDiscoveryFailed
	} else {
		var apiKey string
		apiKey, err = i.Secrets.Open(node.APIKeyNonce, node.APIKeyCiphertext)
		if err == nil {
			catalog, err = i.Preview(ctx, node.ModelNodeInput, apiKey)
		}
	}
	if ctx.Err() != nil {
		return old, ctx.Err()
	}
	code := ""
	if err != nil {
		code = "model_discovery_failed"
	}
	saved, saveErr := i.Store.RefreshModelCatalog(ctx, key.id, key.nodeVersion, key.catalogRevision, catalog.Items, code)
	if saveErr != nil {
		return domain.ModelCatalog{}, saveErr
	}
	if code != "" {
		return saved, ErrDiscoveryFailed
	}
	if i.Wake != nil {
		i.Wake()
	}
	return saved, nil
}

func (i *Inventory) Run(ctx context.Context) {
	if i.Store == nil {
		return
	}
	ticker := time.NewTicker(CatalogRefreshInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		i.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (i *Inventory) refreshDue(ctx context.Context) {
	nodes, err := i.Store.ListModelNodes(ctx)
	if err != nil {
		return
	}
	for _, node := range nodes {
		if ctx.Err() != nil {
			return
		}
		if !node.Enabled || node.ProtocolVersion != domain.ProtocolTK2SD {
			continue
		}
		catalog, err := i.Store.GetModelCatalog(ctx, node.ID)
		if err != nil {
			continue
		}
		if catalog.LastAttemptAt > 0 && i.now().Unix()-catalog.LastAttemptAt < int64(CatalogRefreshInterval/time.Second) {
			continue
		}
		_, _ = i.Refresh(ctx, node.ID, node.Version, catalog.Revision)
	}
}

func cloneCatalog(c domain.ModelCatalog) domain.ModelCatalog {
	c.Items = slices.Clone(c.Items)
	for j := range c.Items {
		capability := &c.Items[j].Capabilities
		capability.Modes = slices.Clone(capability.Modes)
		capability.InputSources = slices.Clone(capability.InputSources)
		capability.Resolutions = slices.Clone(capability.Resolutions)
		for k := range capability.Modes {
			capability.Modes[k].Durations = slices.Clone(capability.Modes[k].Durations)
			capability.Modes[k].Roles = slices.Clone(capability.Modes[k].Roles)
		}
	}
	return c
}
