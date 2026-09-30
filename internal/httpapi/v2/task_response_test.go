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
