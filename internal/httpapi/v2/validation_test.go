package v2

import (
	"encoding/base64"
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

func TestValidateCreateAcceptsPromptAt14000Characters(t *testing.T) {
	request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: strings.Repeat("字", 14000)}}, Resolution: "2K", Duration: 5, Ratio: "16:9"}
	if _, err := ValidateCreate(request, profiles()); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestValidateCreateRejectsPromptOver14000Characters(t *testing.T) {
	request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: strings.Repeat("字", 14001)}}, Resolution: "2K", Duration: 5, Ratio: "16:9"}
	_, err := ValidateCreate(request, profiles())
	if err == nil || !strings.Contains(err.Error(), "不超过 14000 字符") {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestValidateCreateRejectsEmptyPrompt(t *testing.T) {
	tests := []struct {
		name    string
		content []ContentItem
	}{
		{name: "missing text", content: []ContentItem{{Type: "text"}}},
		{name: "empty text", content: []ContentItem{{Type: "text", Text: ""}}},
		{name: "whitespace text", content: []ContentItem{{Type: "text", Text: " \t\r\n"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := CreateRequest{Model: "MiniMax-H3", Content: tt.content, Resolution: "2K", Duration: 5, Ratio: "16:9"}
			_, err := ValidateCreate(request, profiles())
			if err == nil || !strings.Contains(err.Error(), "非空") {
				t.Fatalf("ValidateCreate() error = %v", err)
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

func TestValidateCreateAcceptsExplicitLastFrameOnly(t *testing.T) {
	request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "停在城市夜景"}, image("https://media.example.com/last.png", "last_frame")}, Resolution: "768P", Duration: 4, Ratio: "16:9"}

	got, err := ValidateCreate(request, profiles())
	if err != nil {
		t.Fatal(err)
	}
	if got.Scenario != "i2va" || got.Ratio != "adaptive" || got.Content[1].Role != "last_frame" {
		t.Fatalf("validated = %+v", got)
	}
}

func TestValidateCreateSupportsResolutionTiers(t *testing.T) {
	tests := []struct {
		resolution string
		width      int
	}{
		{resolution: "480P", width: 832},
		{resolution: "768P", width: 1344},
		{resolution: "2K", width: 1920},
	}
	for _, tt := range tests {
		t.Run(tt.resolution, func(t *testing.T) {
			request := CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: tt.resolution, Duration: 5, Ratio: "16:9"}
			got, err := ValidateCreate(request, profiles())
			if err != nil {
				t.Fatalf("ValidateCreate() error = %v", err)
			}
			if got.Width != tt.width {
				t.Fatalf("Width = %d, want %d", got.Width, tt.width)
			}
		})
	}

	request := CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "1K", Duration: 5, Ratio: "16:9"}
	_, err := ValidateCreate(request, profiles())
	if err == nil || !strings.Contains(err.Error(), "480P、768P 或 2K") {
		t.Fatalf("ValidateCreate() error = %v", err)
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

func TestValidateCreateAcceptsBase64Audio(t *testing.T) {
	for _, mediaType := range []string{"audio/wav", "audio/mpeg", "audio/mp3"} {
		t.Run(mediaType, func(t *testing.T) {
			request := CreateRequest{
				Model: "MiniMax-H3",
				Content: []ContentItem{
					{Type: "text", Text: "跟随音频节奏"},
					audio("data:"+mediaType+";base64,UklGRg==", "reference_audio"),
				},
				Resolution: "768P",
				Duration:   4,
				Ratio:      "adaptive",
			}
			if _, err := ValidateCreate(request, profiles()); err != nil {
				t.Fatalf("ValidateCreate() error = %v", err)
			}
		})
	}
}

func TestValidateCreateRejectsInvalidBase64Audio(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{name: "missing payload", source: "data:audio/wav;base64", want: "音频 Base64 格式无效"},
		{name: "empty payload", source: "data:audio/wav;base64,", want: "音频 Base64 格式无效"},
		{name: "invalid encoding", source: "data:audio/wav;base64,%%%", want: "音频 Base64 格式无效"},
		{name: "unsupported mime", source: "data:audio/ogg;base64,T2dnUw==", want: "音频 Base64 类型仅支持 WAV 或 MP3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "保持一致"}, audio(tt.source, "reference_audio")}, Resolution: "768P", Duration: 4, Ratio: "adaptive"}
			_, err := ValidateCreate(request, profiles())
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateCreateLimitsDecodedBase64AudioTo15MiB(t *testing.T) {
	t.Run("exact limit", func(t *testing.T) {
		source := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxDecodedAudioBytes))
		request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "保持一致"}, audio(source, "reference_audio")}, Resolution: "768P", Duration: 4, Ratio: "adaptive"}
		if _, err := ValidateCreate(request, profiles()); err != nil {
			t.Fatalf("ValidateCreate() error = %v", err)
		}
	})
	t.Run("over limit", func(t *testing.T) {
		source := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxDecodedAudioBytes+1))
		request := CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "保持一致"}, audio(source, "reference_audio")}, Resolution: "768P", Duration: 4, Ratio: "adaptive"}
		_, err := ValidateCreate(request, profiles())
		if err == nil || err.Error() != "音频 Base64 单段不能超过 15 MiB" {
			t.Fatalf("error = %v", err)
		}
	})
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
	tests := []struct {
		name string
		req  CreateRequest
		want string
	}{
		{name: "adaptive t2va", req: CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "adaptive"}, want: "ratio"},
		{name: "empty mm file", req: CreateRequest{Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "x"}, image("mm_file://", "reference_image")}, Resolution: "2K", Duration: 5, Ratio: "16:9"}, want: "媒体"},
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

func TestValidateCreateAcceptsMMFileMedia(t *testing.T) {
	items := []ContentItem{
		image("mm_file://image-1", "reference_image"),
		video("mm_file://video-1", "reference_video"),
		audio("mm_file://audio-1", "reference_audio"),
	}
	for _, item := range items {
		request := CreateRequest{
			Model: "MiniMax-H3", Content: []ContentItem{{Type: "text", Text: "保持一致"}, item},
			Resolution: "2K", Duration: 5, Ratio: "16:9",
		}
		if _, err := ValidateCreate(request, profiles()); err != nil {
			t.Fatalf("type=%s error=%v", item.Type, err)
		}
	}
}

func TestValidateCreatePreservesOptionalWatermark(t *testing.T) {
	callback := "https://callback.example.com/events"
	watermark := true
	validated, err := ValidateCreate(CreateRequest{
		Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "16:9",
		CallbackURL: &callback, AIGCWatermark: &watermark,
	}, profiles())
	if err != nil {
		t.Fatal(err)
	}
	if validated.CallbackURL == nil || *validated.CallbackURL != callback || validated.AIGCWatermark == nil || !*validated.AIGCWatermark {
		t.Fatalf("validated=%+v", validated)
	}
	withoutFlag, err := ValidateCreate(CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "16:9"}, profiles())
	if err != nil || withoutFlag.AIGCWatermark != nil {
		t.Fatalf("watermark default=%+v err=%v", withoutFlag.AIGCWatermark, err)
	}
	disabled := false
	withFalse, err := ValidateCreate(CreateRequest{Model: "MiniMax-H3", Content: text(), Resolution: "2K", Duration: 5, Ratio: "16:9", AIGCWatermark: &disabled}, profiles())
	if err != nil || withFalse.AIGCWatermark == nil || *withFalse.AIGCWatermark {
		t.Fatalf("watermark false=%+v err=%v", withFalse.AIGCWatermark, err)
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
	profiles := map[string]config.GenerationProfile{}
	for resolution, width := range map[string]int{"480P": 832, "768P": 1344, "2K": 1920} {
		dimensions := map[string]config.Dimension{}
		for _, ratio := range []string{"adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"} {
			dimensions[ratio] = config.Dimension{Width: width, Height: 768}
		}
		profiles[resolution] = config.GenerationProfile{ModelMode: "high_quality", Steps: 20, Dimensions: dimensions}
	}
	return profiles
}
