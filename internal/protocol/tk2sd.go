package protocol

import (
	"fmt"
	"minimax-h3-tc/internal/domain"
	"strings"
	"unicode/utf8"
)

func normalizeTK2SD(r domain.GenerationRequest, c domain.ModelCapability) (domain.NormalizedRequest, error) {
	if r.Duration == 0 && !r.DurationPresent {
		r.Duration = 5
	}
	if r.Duration <= 0 {
		return domain.NormalizedRequest{}, fmt.Errorf("duration 必须为正整数")
	}
	if len(r.Content) < 1 || len(r.Content) > 13 {
		return domain.NormalizedRequest{}, fmt.Errorf("content 数量必须为 1-13")
	}
	if utf8.RuneCountInString(r.Resolution) > 32 || utf8.RuneCountInString(r.Ratio) > 32 {
		return domain.NormalizedRequest{}, fmt.Errorf("resolution 或 ratio 过长")
	}
	if r.CallbackURL != nil && strings.TrimSpace(*r.CallbackURL) == "" {
		return domain.NormalizedRequest{}, fmt.Errorf("callback_url 不能为空")
	}
	r.Content = append([]domain.GenerationContent(nil), r.Content...)
	mediaCount := 0
	for _, item := range r.Content {
		if item.Type != "text" {
			mediaCount++
		}
	}
	texts, first, images, videos, audios := 0, 0, 0, 0, 0
	prompt := ""
	for i := range r.Content {
		item := &r.Content[i]
		if item.Type == "text" {
			if strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 14000 || item.Role != "" || item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil {
				return domain.NormalizedRequest{}, fmt.Errorf("text 元素无效")
			}
			texts++
			prompt = item.Text
			continue
		}
		media := item.Media()
		if media == nil || item.Text != "" {
			return domain.NormalizedRequest{}, fmt.Errorf("媒体元素结构无效")
		}
		if strings.HasPrefix(media.URL, "mm_file:") || strings.HasPrefix(media.URL, "asset:") || strings.HasPrefix(media.URL, "proxy-input:") {
			return domain.NormalizedRequest{}, fmt.Errorf("tk2sd 不接受节点专属媒体引用")
		}
		if item.Role == "" {
			if item.Type == "image_url" && mediaCount == 1 && c.HasMode("i2va") {
				item.Role = "first_frame"
			} else {
				item.Role = "reference_" + strings.TrimSuffix(item.Type, "_url")
			}
		}
		var err error
		switch item.Type {
		case "image_url":
			if item.VideoURL != nil || item.AudioURL != nil {
				return domain.NormalizedRequest{}, fmt.Errorf("image_url 结构无效")
			}
			images++
			if item.Role == "first_frame" {
				first++
			} else if item.Role != "reference_image" {
				return domain.NormalizedRequest{}, fmt.Errorf("tk2sd 不支持该图片角色")
			}
			err = validateImageSource(media.URL)
		case "video_url":
			if item.ImageURL != nil || item.AudioURL != nil || item.Role != "reference_video" {
				return domain.NormalizedRequest{}, fmt.Errorf("video_url 结构无效")
			}
			videos++
			err = validateVideoSource(media.URL)
		case "audio_url":
			if item.ImageURL != nil || item.VideoURL != nil || item.Role != "reference_audio" {
				return domain.NormalizedRequest{}, fmt.Errorf("audio_url 结构无效")
			}
			audios++
			err = validateAudioSource(media.URL)
		default:
			return domain.NormalizedRequest{}, fmt.Errorf("content.type 无效")
		}
		if err != nil {
			return domain.NormalizedRequest{}, err
		}
	}
	if texts != 1 || images > 9 || videos > 3 || audios > 3 || mediaCount > 12 {
		return domain.NormalizedRequest{}, fmt.Errorf("文本或参考媒体数量无效")
	}
	scenario := "t2va"
	if first > 0 {
		if first != 1 || mediaCount != 1 {
			return domain.NormalizedRequest{}, fmt.Errorf("首帧模式仅支持单张图片")
		}
		scenario = "i2va"
	} else if mediaCount > 0 {
		scenario = "r2va"
	}
	got := normalized(r, scenario, prompt, images, domain.ProtocolTK2SD)
	if !c.Accepts(got.Requirements) {
		return domain.NormalizedRequest{}, domain.ErrModelInput
	}
	return got, nil
}
