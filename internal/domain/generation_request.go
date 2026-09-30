package domain

type GenerationRequest struct {
	Model           string              `json:"model"`
	Content         []GenerationContent `json:"content"`
	Resolution      string              `json:"resolution"`
	Duration        int                 `json:"duration"`
	Ratio           string              `json:"ratio,omitempty"`
	CallbackURL     *string             `json:"callback_url,omitempty"`
	AIGCWatermark   *bool               `json:"aigc_watermark,omitempty"`
	DurationPresent bool                `json:"-"`
}
type GenerationContent struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *MediaURL `json:"image_url,omitempty"`
	VideoURL *MediaURL `json:"video_url,omitempty"`
	AudioURL *MediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}
type MediaURL struct {
	URL string `json:"url"`
}

func (c GenerationContent) Media() *MediaURL {
	switch c.Type {
	case "image_url":
		return c.ImageURL
	case "video_url":
		return c.VideoURL
	case "audio_url":
		return c.AudioURL
	}
	return nil
}

type NormalizedRequest struct {
	Request                             GenerationRequest
	Scenario, Prompt, NormalizerVersion string
	InputImageCount                     int
	Requirements                        TaskRequirements
}
