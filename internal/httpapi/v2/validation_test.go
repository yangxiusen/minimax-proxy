package v2

import (
	"strings"
	"testing"

	"minimax-h3-tc/internal/config"
)

func TestValidateCreateRecognizesOfficialScenarios(t *testing.T) {
	tests := []struct {
		name, scenario, ratio string
		content               []ContentItem
	}{
		{name: "t2va", scenario: "t2va", ratio: "16:9", content: []ContentItem{{Type: "text", Text: "海边日落"}}},
		{name: "i2va", scenario: "i2va", ratio: "adaptive", content: []ContentItem{{Type: "text", Text: "镜头推进"}, image("https://media.example.com/first.png", "first_frame")}},
		{name: "r2va", scenario: "r2va", ratio: "adaptive", content: []ContentItem{{Type: "text", Text: "保持人物一致"}, image("https://media.example.com/ref.png", "reference_image"), video("https://media.example.com/ref.mp4", "reference_video"), audio("https://media.example.com/ref.mp3", "reference_audio")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := CreateRequest{Model: "MiniMax-H3", Content: tt.content, Resolution: "2K", Duration: 5, Ratio: tt.ratio}
			got, err := ValidateCreate(request, profiles())
			if err != nil {
				t.Fatalf("ValidateCreate() error = %v", err)
			}
			if got.Scenario != tt.scenario || got.Ratio != tt.ratio {
				t.Fatalf("validated = %+v", got)
			}
		})
	}
}

func TestValidateCreateNormalizesI2VARatio(t *testing.T) {
	request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "动起来"}, image("https://media.example.com/a.png", "")}, Resolution: "768P", Duration: 4, Ratio: "16:9"}
	got, err := ValidateCreate(request, profiles())
	if err != nil {
		t.Fatal(err)
	}
	if got.Ratio != "adaptive" || got.Content[1].Role != "first_frame" {
		t.Fatalf("validated = %+v", got)
	}
}

func TestValidateCreateAcceptsBase64Image(t *testing.T) {
	request := CreateRequest{
		Model: "MiniMax-H3",
		Content: []ContentItem{
			{Type: "text", Text: "动起来"},
			image("data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "first_frame"),
		},
		Resolution: "768P",
		Duration:   4,
		Ratio:      "adaptive",
	}
	if _, err := ValidateCreate(request, profiles()); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestValidateCreateRejectsNonURLAudioAndVideo(t *testing.T) {
	tests := []struct {
		name string
		item ContentItem
	}{
		{name: "video", item: video("data:video/mp4;base64,AAAA", "reference_video")},
		{name: "audio", item: audio("C:/media/reference.wav", "reference_audio")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "保持一致"}, tt.item}, Resolution: "768P", Duration: 4, Ratio: "adaptive"}
			_, err := ValidateCreate(request, profiles())
			if err == nil || err.Error() != "音频视频必须要上传可以访问的url。" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateCreateRejectsUnsupportedAndInvalidInputs(t *testing.T) {
	callback := "https://callback.example.com"
	watermark := true
	tests := []struct {
		name string
		req  CreateRequest
		want string
	}{
		{name: "callback", req: CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "16:9", CallbackURL: &callback}, want: "callback_url"},
		{name: "watermark", req: CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "16:9", AIGCWatermark: &watermark}, want: "aigc_watermark"},
		{name: "adaptive t2va", req: CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "adaptive"}, want: "ratio"},
		{name: "mm file", req: CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "x"}, image("mm_file://123", "first_frame")}, Resolution: "2K", Duration: 5}, want: "媒体来源"},
		{name: "mixed roles", req: CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "x"}, image("https://a.example/x.png", "first_frame"), image("https://a.example/y.png", "reference_image")}, Resolution: "2K", Duration: 5}, want: "互斥"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateCreate(tt.req, profiles())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contains %q", err, tt.want)
			}
		})
	}
}

func text() []ContentItem { return []ContentItem{{Type: "text", Text: "测试视频"}} }
func image(url, role string) ContentItem {
	return ContentItem{Type: "image_url", ImageURL: &URLValue{URL: url}, Role: role}
}
func video(url, role string) ContentItem {
	return ContentItem{Type: "video_url", VideoURL: &URLValue{URL: url}, Role: role}
}
func audio(url, role string) ContentItem {
	return ContentItem{Type: "audio_url", AudioURL: &URLValue{URL: url}, Role: role}
}

func profiles() map[string]config.GenerationProfile {
	dimensions := map[string]config.Dimension{}
	for _, ratio := range []string{"adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"} {
		dimensions[ratio] = config.Dimension{Width: 1920, Height: 1080}
	}
	return map[string]config.GenerationProfile{"768P": {ModelMode: "high_quality", Steps: 20, Dimensions: dimensions}, "2K": {ModelMode: "custom", Steps: 20, Dimensions: dimensions}}
}
