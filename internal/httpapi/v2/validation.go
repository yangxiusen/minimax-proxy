package v2

import (
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
	if request.Resolution != "768P" && request.Resolution != "2K" {
		return ValidatedRequest{}, fmt.Errorf("resolution 仅支持 768P 或 2K")
	}
	if request.Duration < 4 || request.Duration > 15 {
		return ValidatedRequest{}, fmt.Errorf("duration 必须为 4-15 的整数")
	}
	if request.CallbackURL != nil {
		return ValidatedRequest{}, fmt.Errorf("callback_url 暂不支持")
	}
	if request.AIGCWatermark != nil && *request.AIGCWatermark {
		return ValidatedRequest{}, fmt.Errorf("aigc_watermark=true 暂不支持")
	}

	validated := ValidatedRequest{CreateRequest: request}
	validated.Content = append([]ContentItem(nil), request.Content...)
	textCount, firstCount, lastCount := 0, 0, 0
	referenceImages, referenceVideos, referenceAudios := 0, 0, 0
	for index := range validated.Content {
		item := &validated.Content[index]
		switch item.Type {
		case "text":
			if item.Text == "" || strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 7000 {
				return ValidatedRequest{}, fmt.Errorf("content 必须包含 1 个非空且不超过 7000 字符的 text")
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
			if err := validateMediaURL(item.ImageURL.URL); err != nil {
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
			if err := validateMediaURL(item.VideoURL.URL); err != nil {
				return ValidatedRequest{}, err
			}
			referenceVideos++
		case "audio_url":
			if item.AudioURL == nil || item.ImageURL != nil || item.VideoURL != nil || item.Text != "" || item.Role != "reference_audio" {
				return ValidatedRequest{}, fmt.Errorf("audio_url 必须使用 reference_audio role")
			}
			if err := validateMediaURL(item.AudioURL.URL); err != nil {
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
	profile, ok := profiles[validated.Resolution]
	if !ok {
		return ValidatedRequest{}, fmt.Errorf("resolution 未配置生成 profile")
	}
	dimension, ok := profile.Dimensions[validated.Ratio]
	if !ok {
		return ValidatedRequest{}, fmt.Errorf("ratio 未配置尺寸映射")
	}
	validated.Width, validated.Height = dimension.Width, dimension.Height
	validated.InputImageCount = firstCount + lastCount + referenceImages
	validated.CreateRequest.Ratio = validated.Ratio
	return validated, nil
}

func validateMediaURL(value string) error {
	if strings.HasPrefix(value, "mm_file://") || strings.HasPrefix(strings.ToLower(value), "data:") {
		return fmt.Errorf("该媒体来源暂不支持")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("媒体 URL 必须是无凭据的 HTTP/HTTPS 地址")
	}
	return nil
}

func validRatio(value string) bool {
	switch value {
	case "adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16":
		return true
	default:
		return false
	}
}
