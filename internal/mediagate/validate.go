package mediagate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type manifest struct {
	Video struct {
		Width            int     `json:"width"`
		Height           int     `json:"height"`
		StartSeconds     float64 `json:"start_seconds"`
		DurationSeconds  float64 `json:"duration_seconds"`
		AverageFrameRate string  `json:"avg_frame_rate"`
		FrameCount       int     `json:"frame_count"`
		PTSMonotonic     bool    `json:"pts_monotonic"`
	} `json:"video"`
	Audio struct {
		Present         bool    `json:"present"`
		StartSeconds    float64 `json:"start_seconds"`
		DurationSeconds float64 `json:"duration_seconds"`
	} `json:"audio"`
}

// Validate independently checks the node's media claim against the frozen
// stage parameters before Proxy registers the artifact as successful.
func Validate(stageType string, parameters, raw json.RawMessage) error {
	var value manifest
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("节点阶段媒体清单无法解析")
	}
	video := value.Video
	if video.Width <= 0 || video.Height <= 0 || video.DurationSeconds <= 0 || video.FrameCount <= 0 || !video.PTSMonotonic {
		return errors.New("节点阶段媒体清单缺少有效视频轨参数")
	}
	fps := parseFrameRate(video.AverageFrameRate)
	frameTolerance := 0.05
	if fps > 0 {
		frameTolerance = math.Max(frameTolerance, 1/fps)
	}
	if math.Abs(video.StartSeconds) > 0.05 {
		return errors.New("节点阶段视频起始时间超出 50ms")
	}
	if value.Audio.Present && (math.Abs(value.Audio.StartSeconds-video.StartSeconds) > 0.05 || math.Abs(value.Audio.DurationSeconds-video.DurationSeconds) > frameTolerance) {
		return errors.New("节点阶段媒体音画时间轴不一致")
	}
	switch stageType {
	case "generation":
		var expected struct {
			Width    int     `json:"width"`
			Height   int     `json:"height"`
			Duration float64 `json:"duration"`
		}
		if err := json.Unmarshal(parameters, &expected); err != nil {
			return errors.New("冻结生成参数不可解析")
		}
		if video.Width != expected.Width || video.Height != expected.Height {
			return fmt.Errorf("生成结果尺寸与冻结参数不匹配: expected=%dx%d actual=%dx%d", expected.Width, expected.Height, video.Width, video.Height)
		}
		expectedDuration, validFrameCount := expectedGenerationDuration(expected.Duration, fps, video.FrameCount)
		if !validFrameCount || math.Abs(video.DurationSeconds-expectedDuration) > frameTolerance {
			return fmt.Errorf("生成结果时长与冻结参数不匹配: expected=%.3f actual=%.3f", expected.Duration, video.DurationSeconds)
		}
	case "restoration":
		var expected struct {
			TargetWidth  int `json:"target_width"`
			TargetHeight int `json:"target_height"`
		}
		if err := json.Unmarshal(parameters, &expected); err != nil {
			return errors.New("冻结修复参数不可解析")
		}
		if video.Width != expected.TargetWidth || video.Height != expected.TargetHeight {
			return fmt.Errorf("修复结果尺寸与冻结参数不匹配: expected=%dx%d actual=%dx%d", expected.TargetWidth, expected.TargetHeight, video.Width, video.Height)
		}
	}
	return nil
}

func expectedGenerationDuration(duration, fps float64, frameCount int) (float64, bool) {
	if duration <= 0 || fps <= 0 || frameCount <= 0 {
		return 0, false
	}
	requestedFrames := max(5, int(math.Round(duration*fps)))
	if frameCount == requestedFrames {
		return duration, true
	}
	alignedFrames := requestedFrames + positiveModulo(5-requestedFrames, 17)
	if alignedFrames > requestedFrames && frameCount == alignedFrames {
		return float64(frameCount) / fps, true
	}
	return 0, false
}

func positiveModulo(value, modulus int) int {
	return ((value % modulus) + modulus) % modulus
}

func parseFrameRate(value string) float64 {
	parts := strings.SplitN(value, "/", 2)
	numerator, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || numerator <= 0 {
		return 0
	}
	if len(parts) == 1 {
		return numerator
	}
	denominator, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || denominator <= 0 {
		return 0
	}
	return numerator / denominator
}
