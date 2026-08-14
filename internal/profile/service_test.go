package profile

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"minimax-h3-tc/internal/domain"
)

func TestNormalizeResolutionName(t *testing.T) {
	tests := []struct {
		name, input, display, key string
		wantError                 bool
	}{
		{name: "ascii", input: " 1080P ", display: "1080P", key: "1080p"},
		{name: "hyphen underscore", input: "4K_UHD-1", display: "4K_UHD-1", key: "4k_uhd-1"},
		{name: "han and space", input: "高清 档", display: "高清 档", key: "高清 档"},
		{name: "empty", input: " \t ", wantError: true},
		{name: "too long", input: strings.Repeat("a", 33), wantError: true},
		{name: "tab", input: "高清\t档", wantError: true},
		{name: "slash", input: "4K/UHD", wantError: true},
		{name: "dot", input: "1080.P", wantError: true},
		{name: "emoji", input: "高清档😀", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			display, key, err := domain.NormalizeResolutionName(test.input)
			if test.wantError {
				if !errors.Is(err, domain.ErrInvalidProfileConfig) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || display != test.display || key != test.key {
				t.Fatalf("NormalizeResolutionName(%q)=(%q,%q,%v)", test.input, display, key, err)
			}
		})
	}
}

func TestNormalizeConfigRequiresAllSevenRatios(t *testing.T) {
	config := validConfig()
	if _, err := NormalizeConfig(config); err != nil {
		t.Fatal(err)
	}
	delete(config.Ratios, "adaptive")
	if _, err := NormalizeConfig(config); !errors.Is(err, domain.ErrInvalidProfileConfig) {
		t.Fatalf("missing adaptive error=%v", err)
	}
}

func TestNormalizeConfigRejectsUnsupportedFeatureSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.ProfileConfig)
	}{
		{name: "model mode", mutate: func(c *domain.ProfileConfig) { c.Generation.ModelMode = "custom" }},
		{name: "cache mode", mutate: func(c *domain.ProfileConfig) { c.Generation.CacheMode = "easycache+te_speed" }},
		{name: "too many loras", mutate: func(c *domain.ProfileConfig) {
			c.LoRAs = append(c.LoRAs, domain.LoRAProfile{Name: "fifth.safetensors", Strength: 1})
		}},
		{name: "rife scale", mutate: func(c *domain.ProfileConfig) { c.Interpolation.Scale = 4 }},
		{name: "restoration dimensions", mutate: func(c *domain.ProfileConfig) {
			mapping := c.Ratios["16:9"]
			mapping.TargetWidth++
			c.Ratios["16:9"] = mapping
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			if _, err := NormalizeConfig(config); !errors.Is(err, domain.ErrInvalidProfileConfig) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestServiceImmediateCRUDByResolution(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, nil, func(prefix string) string { return prefix + "1" })
	created, err := service.Create(context.Background(), "2K", validConfig(), "creator")
	if err != nil {
		t.Fatal(err)
	}
	if created.Resolution != "2K" || created.RowVersion != 1 || created.CreatedBy != "creator" || created.UpdatedBy != "creator" {
		t.Fatalf("created=%+v", created)
	}
	if _, err := service.Create(context.Background(), "2K", validConfig(), "creator"); !errors.Is(err, domain.ErrProfileKeyConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	updatedConfig := created.Config
	updatedConfig.Generation.Steps = 9
	updated, err := service.Update(context.Background(), created.ID, created.RowVersion, updatedConfig, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowVersion != 2 || updated.Config.Generation.Steps != 9 || updated.UpdatedBy != "editor" {
		t.Fatalf("updated=%+v", updated)
	}
	byResolution, err := service.GetByResolution(context.Background(), "2K")
	if err != nil || byResolution.ID != created.ID || byResolution.Config.Generation.Steps != 9 {
		t.Fatalf("by resolution=%+v err=%v", byResolution, err)
	}
	if err := service.Delete(context.Background(), created.ID, created.RowVersion); !errors.Is(err, domain.ErrProfileVersionConflict) {
		t.Fatalf("stale delete error=%v", err)
	}
	if err := service.Delete(context.Background(), created.ID, updated.RowVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetByResolution(context.Background(), "2K"); !errors.Is(err, domain.ErrProfileNotFound) {
		t.Fatalf("get deleted error=%v", err)
	}
}

func TestServiceCreatesAndLooksUpDynamicResolutionCaseInsensitively(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, nil, func(prefix string) string { return prefix + "dynamic" })
	config := validConfig()
	config.Resolution = " 1080P "
	created, err := service.Create(context.Background(), config.Resolution, config, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if created.Resolution != "1080P" || created.Config.Resolution != "1080P" {
		t.Fatalf("created=%+v", created)
	}
	got, err := service.GetByResolution(context.Background(), " 1080p ")
	if err != nil || got.ID != created.ID || got.Resolution != "1080P" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestServiceRejectsResolutionMismatchOnCreateAndUpdate(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, nil, func(prefix string) string { return prefix + "1" })
	config := validConfig()
	config.Resolution = "480P"
	if _, err := service.Create(context.Background(), "2K", config, "admin"); !errors.Is(err, domain.ErrInvalidProfileConfig) {
		t.Fatalf("create mismatch error=%v", err)
	}
	config.Resolution = "2K"
	created, err := service.Create(context.Background(), "2K", config, "admin")
	if err != nil {
		t.Fatal(err)
	}
	changed := created.Config
	changed.Resolution = "480P"
	if _, err := service.Update(context.Background(), created.ID, created.RowVersion, changed, "admin"); !errors.Is(err, domain.ErrInvalidProfileConfig) {
		t.Fatalf("update mismatch error=%v", err)
	}
}

func TestServiceListIsResolutionOrdered(t *testing.T) {
	repository := newMemoryRepository()
	service := New(repository, nil, func(prefix string) string { return prefix + repository.nextID() })
	for _, resolution := range []string{"zeta", "2K", "480P", "高清档", "alpha", "768P"} {
		config := validConfig()
		config.Resolution = resolution
		if _, err := service.Create(context.Background(), resolution, config, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	items, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(items))
	for index := range items {
		got[index] = items[index].Resolution
	}
	if want := []string{"480P", "768P", "2K", "alpha", "zeta", "高清档"}; !equalStrings(got, want) {
		t.Fatalf("resolutions=%v", got)
	}
}

func validConfig() domain.ProfileConfig {
	ratios := make(map[string]domain.RatioMapping, len(domain.ProfileRatios))
	base := map[string][2]int{
		"adaptive": {832, 480}, "21:9": {1120, 480}, "16:9": {832, 480}, "4:3": {640, 480},
		"1:1": {480, 480}, "3:4": {480, 640}, "9:16": {480, 832},
	}
	for ratio, size := range base {
		ratios[ratio] = domain.RatioMapping{BaseWidth: size[0], BaseHeight: size[1], TargetWidth: size[0] * 3, TargetHeight: size[1] * 3}
	}
	return domain.ProfileConfig{
		Resolution: "2K",
		Generation: domain.GenerationProfile{ModelMode: "high_quality", Steps: 8, SageAttention: "auto", CacheMode: "easycache"},
		Ratios:     ratios,
		LoRAs: []domain.LoRAProfile{
			{Name: "one.safetensors", Strength: 1}, {Name: "two.safetensors", Strength: 0.8},
			{Name: "three.safetensors", Strength: 0.6}, {Name: "four.safetensors", Strength: 0.4},
		},
		Interpolation: domain.InterpolationProfile{Enabled: true, Engine: "rife", Scale: 2},
		Restoration:   domain.RestorationProfile{Enabled: true, Engine: "seedvr2", Scale: 3},
	}
}

type memoryRepository struct {
	profiles map[string]domain.ModelRequestProfile
	serial   int
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{profiles: map[string]domain.ModelRequestProfile{}}
}

func (repository *memoryRepository) nextID() string {
	repository.serial++
	return string(rune('0' + repository.serial))
}

func (repository *memoryRepository) CreateProfile(_ context.Context, input domain.ModelRequestProfile) (domain.ModelRequestProfile, error) {
	for _, item := range repository.profiles {
		if item.ResolutionKey == input.ResolutionKey {
			return domain.ModelRequestProfile{}, domain.ErrProfileKeyConflict
		}
	}
	input.RowVersion = 1
	repository.profiles[input.ID] = input
	return input, nil
}

func (repository *memoryRepository) GetProfile(_ context.Context, id string) (domain.ModelRequestProfile, error) {
	item, ok := repository.profiles[id]
	if !ok {
		return domain.ModelRequestProfile{}, domain.ErrProfileNotFound
	}
	return item, nil
}

func (repository *memoryRepository) GetProfileByResolution(_ context.Context, resolution string) (domain.ModelRequestProfile, error) {
	for _, item := range repository.profiles {
		if item.ResolutionKey == resolution {
			return item, nil
		}
	}
	return domain.ModelRequestProfile{}, domain.ErrProfileNotFound
}

func (repository *memoryRepository) ListProfiles(context.Context) ([]domain.ModelRequestProfile, error) {
	items := make([]domain.ModelRequestProfile, 0, len(repository.profiles))
	for _, item := range repository.profiles {
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Resolution < items[right].Resolution })
	return items, nil
}

func (repository *memoryRepository) UpdateProfile(_ context.Context, id string, version int64, configJSON, configHash, administrator string) (domain.ModelRequestProfile, error) {
	item, ok := repository.profiles[id]
	if !ok {
		return domain.ModelRequestProfile{}, domain.ErrProfileNotFound
	}
	if item.RowVersion != version {
		return domain.ModelRequestProfile{}, domain.ErrProfileVersionConflict
	}
	item.ConfigJSON, item.ConfigHash, item.UpdatedBy, item.RowVersion = configJSON, configHash, administrator, item.RowVersion+1
	repository.profiles[id] = item
	return item, nil
}

func (repository *memoryRepository) DeleteProfile(_ context.Context, id string, version int64) error {
	item, ok := repository.profiles[id]
	if !ok {
		return domain.ErrProfileNotFound
	}
	if item.RowVersion != version {
		return domain.ErrProfileVersionConflict
	}
	delete(repository.profiles, id)
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
