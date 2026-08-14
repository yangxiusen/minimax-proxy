package gradio

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/httpapi/v2"
)

type FileData struct {
	Path string   `json:"path,omitempty"`
	URL  string   `json:"url,omitempty"`
	Meta FileMeta `json:"meta"`
}
type FileMeta struct {
	Type string `json:"_type"`
}

func BuildArguments(request v2.ValidatedRequest, profile config.GenerationProfile) ([]any, error) {
	mode := map[string]string{
		"t2va": "文生视频",
		"i2va": "图生视频（首帧/可选尾帧）",
		"r2va": "全能参考生成视频",
	}[request.Scenario]
	if mode == "" {
		return nil, errors.New("生成场景无效")
	}
	customModel, customModelHigh := profile.CustomModel, profile.CustomModelHigh
	if customModel == "" {
		customModel = "__follow_model_mode__"
	}
	if customModelHigh == "" {
		customModelHigh = "__follow_model_mode__"
	}
	if profile.FPS != 0 && profile.FPS != 24 {
		return nil, fmt.Errorf("legacy Gradio upstream only supports 24 fps; use the node API for %d fps", profile.FPS)
	}
	args := make([]any, 32)
	args[0], args[1], args[2], args[3], args[4] = mode, request.Prompt, "批量单图视频", nil, ""
	args[7], args[8], args[9], args[10], args[11] = "match", profile.ModelMode, customModel, customModelHigh, profile.EasyCache
	args[12], args[13], args[14], args[15], args[16] = request.Width, request.Height, request.Duration, profile.Steps, -1
	imageIndex, videoIndex, audioIndex := 17, 26, 29
	for _, item := range request.Content {
		var target *int
		var rawURL string
		switch item.Role {
		case "first_frame":
			args[5], rawURL = imageData(item.ImageURL.URL), item.ImageURL.URL
		case "last_frame":
			args[6], rawURL = imageData(item.ImageURL.URL), item.ImageURL.URL
		case "reference_image":
			target, rawURL = &imageIndex, item.ImageURL.URL
		case "reference_video":
			target, rawURL = &videoIndex, item.VideoURL.URL
		case "reference_audio":
			target, rawURL = &audioIndex, item.AudioURL.URL
		}
		if target != nil {
			if item.Role == "reference_image" {
				args[*target] = imageData(rawURL)
			} else {
				args[*target] = fileData(rawURL)
			}
			*target++
		}
	}
	return args, nil
}

func fileData(value string) FileData {
	return FileData{Path: value, Meta: FileMeta{Type: "gradio.FileData"}}
}

func imageData(value string) FileData {
	if strings.HasPrefix(value, "data:image/") {
		return FileData{URL: value, Meta: FileMeta{Type: "gradio.FileData"}}
	}
	return fileData(value)
}

func GalleryURLs(gallery any) []string {
	set := map[string]struct{}{}
	collectURLs(gallery, set)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func UniqueNewVideo(before []string, gallery any) (string, error) {
	old := map[string]struct{}{}
	for _, value := range before {
		if normalized, err := normalizeVideoURL(value); err == nil {
			old[normalized] = struct{}{}
		}
	}
	newVideos := make([]string, 0)
	for _, value := range GalleryURLs(gallery) {
		normalized, err := normalizeVideoURL(value)
		if err != nil {
			continue
		}
		if _, exists := old[normalized]; !exists {
			newVideos = append(newVideos, value)
		}
	}
	if len(newVideos) != 1 {
		return "", fmt.Errorf("%w: 新增结果数量为 %d", ErrResultAmbiguous, len(newVideos))
	}
	return newVideos[0], nil
}

func RewritePublicURL(value string, privateBase, publicBase *url.URL) (string, error) {
	internal, err := url.Parse(value)
	if err != nil || internal.Host == "" {
		return "", errors.New("结果 URL 无效")
	}
	if !strings.EqualFold(internal.Host, privateBase.Host) {
		return "", errors.New("结果 URL 不属于当前上游")
	}
	result := *publicBase
	result.Path = path.Join(publicBase.Path, internal.Path)
	if strings.HasSuffix(internal.Path, "/") {
		result.Path += "/"
	}
	result.RawPath = ""
	result.RawQuery = internal.RawQuery
	result.Fragment = ""
	return result.String(), nil
}

func collectURLs(value any, result map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectURLs(item, result)
		}
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok && (key == "url" || key == "path") && isVideoURL(text) {
				result[text] = struct{}{}
			}
			collectURLs(item, result)
		}
	}
}

func isVideoURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	extension := strings.ToLower(path.Ext(parsed.Path))
	return extension == ".mp4" || extension == ".mov"
}

func normalizeVideoURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", errors.New("URL 无效")
	}
	parsed.Scheme, parsed.Host = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
	parsed.Path = path.Clean(parsed.Path)
	parsed.Fragment = ""
	return parsed.String(), nil
}
