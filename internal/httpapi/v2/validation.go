package v2

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"minimax-h3-tc/internal/config"
)

type CreateRequest struct {
	Model         string        `json:"model"`
	Content       []ContentItem `json:"content"`
	Resolution    string        `json:"resolution"`
	Duration      int           `json:"duration"`
	Ratio         string        `json:"ratio,omitempty"`
	CallbackURL   *string       `json:"callback_url,omitempty"`
	AIGCWatermark *bool         `json:"aigc_watermark,omitempty"`
}

type ContentItem struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *URLValue `json:"image_url,omitempty"`
	VideoURL *URLValue `json:"video_url,omitempty"`
	AudioURL *URLValue `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}

type URLValue struct {
	URL string `json:"url"`
}

const (
	MaxDecodedImageBytes = 30 << 20
	MaxDecodedVideoBytes = 50 << 20
	MaxDecodedAudioBytes = 15 << 20
)

type ValidatedRequest struct {
	CreateRequest
	Scenario, Prompt string
	Width, Height    int
	InputImageCount  int
}

func ValidateCreate(request CreateRequest, profiles map[string]config.GenerationProfile) (ValidatedRequest, error) {
	if request.Model != "MiniMax-H3" {
		return ValidatedRequest{}, fmt.Errorf("model 仅支持 MiniMax-H3")
	}
	if len(request.Content) < 1 || len(request.Content) > 16 {
		return ValidatedRequest{}, fmt.Errorf("content 数量必须为 1-16")
	}
	if request.Duration < 4 || request.Duration > 15 {
		return ValidatedRequest{}, fmt.Errorf("duration 必须为 4-15 的整数")
	}
	if request.CallbackURL != nil && strings.TrimSpace(*request.CallbackURL) == "" {
		return ValidatedRequest{}, fmt.Errorf("callback_url 不能为空")
	}
	validated := ValidatedRequest{CreateRequest: request}
	validated.Content = append([]ContentItem(nil), request.Content...)
	textCount, firstCount, lastCount := 0, 0, 0
	referenceImages, referenceVideos, referenceAudios := 0, 0, 0
	for index := range validated.Content {
		item := &validated.Content[index]
		switch item.Type {
		case "text":
			if item.Text == "" || strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 14000 {
				return ValidatedRequest{}, fmt.Errorf("content 必须包含 1 个非空且不超过 14000 字符的 text")
			}
			if item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil || item.Role != "" {
				return ValidatedRequest{}, fmt.Errorf("text 元素包含不允许的字段")
			}
			textCount++
			validated.Prompt = item.Text
		case "image_url":
			if item.ImageURL == nil || item.VideoURL != nil || item.AudioURL != nil || item.Text != "" {
				return ValidatedRequest{}, fmt.Errorf("image_url 元素结构无效")
			}
			if err := validateImageSource(item.ImageURL.URL); err != nil {
				return ValidatedRequest{}, err
			}
			if item.Role == "" {
				item.Role = "first_frame"
			}
			switch item.Role {
			case "first_frame":
				firstCount++
			case "last_frame":
				lastCount++
			case "reference_image":
				referenceImages++
			default:
				return ValidatedRequest{}, fmt.Errorf("image_url role 无效")
			}
		case "video_url":
			if item.VideoURL == nil || item.ImageURL != nil || item.AudioURL != nil || item.Text != "" || item.Role != "reference_video" {
				return ValidatedRequest{}, fmt.Errorf("video_url 必须使用 reference_video role")
			}
			if err := validateVideoSource(item.VideoURL.URL); err != nil {
				return ValidatedRequest{}, err
			}
			referenceVideos++
		case "audio_url":
			if item.AudioURL == nil || item.ImageURL != nil || item.VideoURL != nil || item.Text != "" || item.Role != "reference_audio" {
				return ValidatedRequest{}, fmt.Errorf("audio_url 必须使用 reference_audio role")
			}
			if err := validateAudioSource(item.AudioURL.URL); err != nil {
				return ValidatedRequest{}, err
			}
			referenceAudios++
		default:
			return ValidatedRequest{}, fmt.Errorf("content.type 无效")
		}
	}
	if textCount != 1 {
		return ValidatedRequest{}, fmt.Errorf("content 必须包含且只能包含 1 个 text")
	}
	if firstCount > 1 || lastCount > 1 {
		return ValidatedRequest{}, fmt.Errorf("首帧和尾帧最多各 1 张")
	}
	if referenceImages > 9 || referenceVideos > 3 || referenceAudios > 3 {
		return ValidatedRequest{}, fmt.Errorf("参考媒体数量超过限制")
	}
	hasFrames := firstCount+lastCount > 0
	hasReferences := referenceImages+referenceVideos+referenceAudios > 0
	if hasFrames && hasReferences {
		return ValidatedRequest{}, fmt.Errorf("图生视频与多模态参考角色互斥")
	}
	switch {
	case hasFrames:
		validated.Scenario, validated.Ratio = "i2va", "adaptive"
	case hasReferences:
		validated.Scenario = "r2va"
		if validated.Ratio == "" {
			validated.Ratio = "adaptive"
		}
	default:
		validated.Scenario = "t2va"
		if validated.Ratio == "" || validated.Ratio == "adaptive" {
			return ValidatedRequest{}, fmt.Errorf("t2va 的 ratio 必填且不能为 adaptive")
		}
	}
	if !validRatio(validated.Ratio) {
		return ValidatedRequest{}, fmt.Errorf("ratio 无效")
	}
	if profiles != nil {
		profile, ok := profiles[validated.Resolution]
		if !ok {
			return ValidatedRequest{}, fmt.Errorf("resolution 仅支持 480P、768P 或 2K")
		}
		dimension, ok := profile.Dimensions[validated.Ratio]
		if !ok {
			return ValidatedRequest{}, fmt.Errorf("ratio 未配置尺寸映射")
		}
		validated.Width, validated.Height = dimension.Width, dimension.Height
	}
	validated.InputImageCount = firstCount + lastCount + referenceImages
	validated.CreateRequest.Ratio = validated.Ratio
	return validated, nil
}

func validateImageSource(value string) error {
	if strings.HasPrefix(value, "data:") {
		_, _, _, err := parseDataURI(value, "图片", MaxDecodedImageBytes, "单张", map[string]string{
			"image/jpeg": "image/jpeg", "image/jpg": "image/jpeg", "image/png": "image/png", "image/webp": "image/webp",
		})
		return err
	}
	if strings.HasPrefix(value, "mm_file://") {
		if strings.TrimSpace(strings.TrimPrefix(value, "mm_file://")) == "" {
			return fmt.Errorf("mm_file 媒体地址缺少文件标识")
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("媒体 URL 必须是无凭据的 HTTP/HTTPS 地址")
	}
	return nil
}

func validateVideoSource(value string) error {
	if strings.HasPrefix(value, "data:") {
		_, _, _, err := parseDataURI(value, "视频", MaxDecodedVideoBytes, "单段", map[string]string{"video/mp4": "video/mp4"})
		if err != nil && strings.Contains(err.Error(), "类型") {
			return fmt.Errorf("视频 Base64 类型仅支持 MP4")
		}
		return err
	}
	return validateAccessibleMediaURL(value)
}

func validateAccessibleMediaURL(value string) error {
	if strings.HasPrefix(value, "mm_file://") {
		if strings.TrimSpace(strings.TrimPrefix(value, "mm_file://")) == "" {
			return fmt.Errorf("mm_file 媒体地址缺少文件标识")
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("音频视频必须要上传可以访问的url。")
	}
	return nil
}

func validateAudioSource(value string) error {
	_, _, isDataURI, err := ParseAudioDataURI(value)
	if isDataURI {
		return err
	}
	return validateAccessibleMediaURL(value)
}

func ParseAudioDataURI(value string) (string, []byte, bool, error) {
	mediaType, decoded, isDataURI, err := parseDataURI(value, "音频", MaxDecodedAudioBytes, "单段", map[string]string{
		"audio/wav": "audio/wav", "audio/mpeg": "audio/mpeg", "audio/mp3": "audio/mp3",
	})
	if err != nil && strings.Contains(err.Error(), "类型") {
		return "", nil, isDataURI, fmt.Errorf("音频 Base64 类型仅支持 WAV 或 MP3")
	}
	return mediaType, decoded, isDataURI, err
}

func parseDataURI(value, label string, maxBytes int, unit string, supported map[string]string) (string, []byte, bool, error) {
	if !strings.HasPrefix(value, "data:") {
		return "", nil, false, nil
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || encoded == "" {
		return "", nil, true, fmt.Errorf("%s Base64 格式无效", label)
	}
	mediaType, encoding, ok := strings.Cut(strings.TrimPrefix(header, "data:"), ";")
	if !ok || !strings.EqualFold(encoding, "base64") {
		return "", nil, true, fmt.Errorf("%s Base64 格式无效", label)
	}
	mediaType = strings.ToLower(mediaType)
	normalized, ok := supported[mediaType]
	if !ok {
		return "", nil, true, fmt.Errorf("%s Base64 类型不支持", label)
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxBytes) {
		return "", nil, true, fmt.Errorf("%s Base64 %s不能超过 %d MiB", label, unit, maxBytes>>20)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", nil, true, fmt.Errorf("%s Base64 格式无效", label)
	}
	if len(decoded) > maxBytes {
		return "", nil, true, fmt.Errorf("%s Base64 %s不能超过 %d MiB", label, unit, maxBytes>>20)
	}
	return normalized, decoded, true, nil
}

func validRatio(value string) bool {
	switch value {
	case "adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16":
		return true
	default:
		return false
	}
}
