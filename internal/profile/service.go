package profile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"minimax-h3-tc/internal/domain"
)

type Repository interface {
	CreateProfile(context.Context, domain.ModelRequestProfile) (domain.ModelRequestProfile, error)
	GetProfile(context.Context, string) (domain.ModelRequestProfile, error)
	GetProfileByResolution(context.Context, string) (domain.ModelRequestProfile, error)
	ListProfiles(context.Context) ([]domain.ModelRequestProfile, error)
	UpdateProfile(context.Context, string, int64, string, string, string) (domain.ModelRequestProfile, error)
	DeleteProfile(context.Context, string, int64) error
}

type IDGenerator func(prefix string) string

type Service struct {
	repository Repository
	newID      IDGenerator
}

func New(repository Repository, _ NodeMatcher, newID IDGenerator) *Service {
	if newID == nil {
		newID = randomID
	}
	return &Service{repository: repository, newID: newID}
}

func (s *Service) Create(ctx context.Context, resolution string, config domain.ProfileConfig, administrator string) (domain.ModelRequestProfile, error) {
	display, key, err := domain.NormalizeResolutionName(resolution)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	_, configKey, err := domain.NormalizeResolutionName(config.Resolution)
	if err != nil || configKey != key {
		return domain.ModelRequestProfile{}, invalid("resolution 与配置不一致")
	}
	config.Resolution = display
	normalized, configJSON, hash, err := normalizeAndEncode(config)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	profile, err := s.repository.CreateProfile(ctx, domain.ModelRequestProfile{ID: s.newID("profile_"), Resolution: display, ResolutionKey: key, ConfigJSON: configJSON, ConfigHash: hash, CreatedBy: administrator, UpdatedBy: administrator})
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	profile.Config = normalized
	return profile, nil
}

func (s *Service) Update(ctx context.Context, profileID string, rowVersion int64, config domain.ProfileConfig, administrator string) (domain.ModelRequestProfile, error) {
	current, err := s.repository.GetProfile(ctx, profileID)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	if current.RowVersion != rowVersion {
		return domain.ModelRequestProfile{}, domain.ErrProfileVersionConflict
	}
	_, configKey, configNameErr := domain.NormalizeResolutionName(config.Resolution)
	_, currentKey, currentNameErr := domain.NormalizeResolutionName(current.Resolution)
	if configNameErr != nil || currentNameErr != nil || configKey != currentKey {
		return domain.ModelRequestProfile{}, invalid("resolution 不可修改")
	}
	config.Resolution = current.Resolution
	normalized, configJSON, hash, err := normalizeAndEncode(config)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	updated, err := s.repository.UpdateProfile(ctx, profileID, rowVersion, configJSON, hash, administrator)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	updated.Config = normalized
	return updated, nil
}

func (s *Service) Get(ctx context.Context, profileID string) (domain.ModelRequestProfile, error) {
	profile, err := s.repository.GetProfile(ctx, profileID)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	return decodeProfile(profile)
}

func (s *Service) GetByResolution(ctx context.Context, resolution string) (domain.ModelRequestProfile, error) {
	_, key, err := domain.NormalizeResolutionName(resolution)
	if err != nil {
		return domain.ModelRequestProfile{}, domain.ErrProfileNotFound
	}
	profile, err := s.repository.GetProfileByResolution(ctx, key)
	if err != nil {
		return domain.ModelRequestProfile{}, err
	}
	return decodeProfile(profile)
}

func (s *Service) List(ctx context.Context) ([]domain.ModelRequestProfile, error) {
	items, err := s.repository.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index], err = decodeProfile(items[index])
		if err != nil {
			return nil, err
		}
	}
	order := map[string]int{"480p": 1, "768p": 2, "2k": 3}
	sort.SliceStable(items, func(left, right int) bool {
		_, leftKey, _ := domain.NormalizeResolutionName(items[left].Resolution)
		_, rightKey, _ := domain.NormalizeResolutionName(items[right].Resolution)
		leftOrder, leftBuiltIn := order[leftKey]
		rightOrder, rightBuiltIn := order[rightKey]
		if leftBuiltIn || rightBuiltIn {
			if leftBuiltIn && rightBuiltIn {
				return leftOrder < rightOrder
			}
			return leftBuiltIn
		}
		if leftKey == rightKey {
			return items[left].ID < items[right].ID
		}
		return leftKey < rightKey
	})
	return items, nil
}

func (s *Service) Delete(ctx context.Context, profileID string, rowVersion int64) error {
	return s.repository.DeleteProfile(ctx, profileID, rowVersion)
}

func NormalizeConfig(config domain.ProfileConfig) (domain.ProfileConfig, error) {
	display, _, err := domain.NormalizeResolutionName(config.Resolution)
	if err != nil {
		return domain.ProfileConfig{}, err
	}
	config.Resolution = display
	if len(config.Ratios) != len(domain.ProfileRatios) {
		return domain.ProfileConfig{}, invalid("ratios 必须完整包含 adaptive 和六种固定比例")
	}
	if config.Generation.ModelMode != "high_quality" && config.Generation.ModelMode != "low_vram" {
		return domain.ProfileConfig{}, invalid("model_mode 只能为 high_quality 或 low_vram")
	}
	if config.Generation.Steps < 1 || config.Generation.Steps > 100 {
		return domain.ProfileConfig{}, invalid("generation steps 超出范围")
	}
	if config.Generation.SageAttention != "auto" && config.Generation.SageAttention != "on" && config.Generation.SageAttention != "off" {
		return domain.ProfileConfig{}, invalid("sage_attention 无效")
	}
	if config.Generation.CacheMode != "off" && config.Generation.CacheMode != "easycache" && config.Generation.CacheMode != "te_speed" {
		return domain.ProfileConfig{}, invalid("cache_mode 只能为 off、easycache 或 te_speed")
	}
	if len(config.LoRAs) > 4 {
		return domain.ProfileConfig{}, invalid("LoRA 最多配置 4 个")
	}
	for index := range config.LoRAs {
		config.LoRAs[index].Name = strings.TrimSpace(config.LoRAs[index].Name)
		name := config.LoRAs[index].Name
		extension := strings.ToLower(filepath.Ext(name))
		if name == "" || name != filepath.Base(name) || (extension != ".safetensors" && extension != ".pt" && extension != ".pth") || config.LoRAs[index].Strength < -2 || config.LoRAs[index].Strength > 2 {
			return domain.ProfileConfig{}, invalid("LoRA 名称或强度无效")
		}
	}
	if config.Interpolation.Enabled {
		if config.Interpolation.Engine != "rife" || config.Interpolation.Scale != 2 {
			return domain.ProfileConfig{}, invalid("RIFE 补帧仅允许 2 倍")
		}
	}
	if config.Restoration.Enabled && ((config.Restoration.Engine != "seedvr2" && config.Restoration.Engine != "flashvsr") || config.Restoration.Scale < 1 || config.Restoration.Scale > 4) {
		return domain.ProfileConfig{}, invalid("高清修复配置无效")
	}
	for _, ratio := range domain.ProfileRatios {
		mapping, exists := config.Ratios[ratio]
		if !exists || !validDimension(mapping.BaseWidth, 256, 4096) || !validDimension(mapping.BaseHeight, 256, 4096) || !validDimension(mapping.TargetWidth, 256, 8192) || !validDimension(mapping.TargetHeight, 256, 8192) {
			return domain.ProfileConfig{}, invalid("比例 %s 的尺寸无效", ratio)
		}
		scale := 1
		if config.Restoration.Enabled {
			scale = config.Restoration.Scale
		}
		if mapping.TargetWidth != mapping.BaseWidth*scale || mapping.TargetHeight != mapping.BaseHeight*scale {
			return domain.ProfileConfig{}, invalid("比例 %s 的目标尺寸必须等于基础尺寸乘高清修复倍率", ratio)
		}
		if ratio != "adaptive" && !ratioMatchesDimensions(ratio, mapping.BaseWidth, mapping.BaseHeight) {
			return domain.ProfileConfig{}, invalid("比例 %s 与基础尺寸不匹配", ratio)
		}
	}
	return config, nil
}

func normalizeAndEncode(config domain.ProfileConfig) (domain.ProfileConfig, string, string, error) {
	normalized, err := NormalizeConfig(config)
	if err != nil {
		return domain.ProfileConfig{}, "", "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return domain.ProfileConfig{}, "", "", err
	}
	digest := sha256.Sum256(encoded)
	return normalized, string(encoded), "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeProfile(profile domain.ModelRequestProfile) (domain.ModelRequestProfile, error) {
	if err := json.Unmarshal([]byte(profile.ConfigJSON), &profile.Config); err != nil {
		return domain.ModelRequestProfile{}, errors.New("请求配置 JSON 损坏")
	}
	return profile, nil
}

func validDimension(value, minimum, maximum int) bool {
	return value >= minimum && value <= maximum && value%32 == 0
}

func ratioMatchesDimensions(ratio string, width, height int) bool {
	parts := strings.Split(ratio, ":")
	if len(parts) != 2 {
		return false
	}
	left, errLeft := strconvAtoi(parts[0])
	right, errRight := strconvAtoi(parts[1])
	if errLeft != nil || errRight != nil {
		return false
	}
	actual := float64(width) / float64(height)
	expected := float64(left) / float64(right)
	return math.Abs(actual-expected)/expected <= 0.05
}

func strconvAtoi(value string) (int, error) {
	var result int
	_, err := fmt.Sscanf(value, "%d", &result)
	return result, err
}

func invalid(format string, values ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidProfileConfig, fmt.Sprintf(format, values...))
}

func randomID(prefix string) string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(buffer)
}
