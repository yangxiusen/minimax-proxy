package v2

import (
	"context"
	"minimax-h3-tc/internal/domain"
	"testing"
)

func TestTKResponseNeverUsesRequestedMetadata(t *testing.T) {
	h := &handler{}
	task := domain.Task{TaskID: "t", Model: "m", ProtocolVersion: domain.ProtocolTK2SD, Status: domain.StatusSucceeded, Resolution: "1080p", Duration: 5, RatioRequested: "16:9", ResultPublicURL: "https://example.com/result"}
	got, err := h.mapTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration != nil || got.Resolution != nil || got.Ratio != nil || got.Usage != nil {
		t.Fatalf("fabricated metadata: %+v", got)
	}
	task.ResultMetadataJSON = `{"duration":5.062,"ratio":"9:16","resolution":"720p"}`
	got, err = h.mapTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration == nil || *got.Duration != 5.062 {
		t.Fatalf("actual duration: %+v", got)
	}
}

func TestTKResponsePreservesSpecificInputFailure(t *testing.T) {
	h := &handler{}
	task := domain.Task{TaskID: "t", Model: "m", ProtocolVersion: domain.ProtocolTK2SD, Status: domain.StatusFailed, ErrorCode: "tk2sd_input_source_http_error", ErrorMessage: "对象存储：素材下载失败（HTTP 403）；content[1] image_url"}
	got, err := h.mapTask(context.Background(), task)
	if err != nil || got.Error == nil || got.Error.Code != task.ErrorCode || got.Error.Message != task.ErrorMessage {
		t.Fatalf("specific input failure lost: %+v, %v", got.Error, err)
	}
}
