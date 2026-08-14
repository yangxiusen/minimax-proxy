package mediagate

import (
	"encoding/json"
	"testing"
)

func TestValidateRejectsFrozenGenerationDimensionMismatch(t *testing.T) {
	parameters := json.RawMessage(`{"width":832,"height":480,"duration":5}`)
	manifest := json.RawMessage(`{"video":{"width":640,"height":480,"start_seconds":0,"duration_seconds":5,"avg_frame_rate":"24/1","frame_count":120,"pts_monotonic":true},"audio":{"present":false}}`)

	if err := Validate("generation", parameters, manifest); err == nil {
		t.Fatal("expected dimension mismatch")
	}
}

func TestValidateAcceptsSynchronizedGeneration(t *testing.T) {
	parameters := json.RawMessage(`{"width":832,"height":480,"duration":5}`)
	manifest := json.RawMessage(`{"video":{"width":832,"height":480,"start_seconds":0,"duration_seconds":5,"avg_frame_rate":"24/1","frame_count":120,"pts_monotonic":true},"audio":{"present":true,"start_seconds":0,"duration_seconds":5}}`)

	if err := Validate("generation", parameters, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsMiniMaxAlignedGenerationTail(t *testing.T) {
	parameters := json.RawMessage(`{"width":832,"height":480,"duration":4}`)
	manifest := json.RawMessage(`{"video":{"width":832,"height":480,"start_seconds":0,"duration_seconds":4.458333,"avg_frame_rate":"24/1","frame_count":107,"pts_monotonic":true},"audio":{"present":true,"start_seconds":0,"duration_seconds":4.45}}`)

	if err := Validate("generation", parameters, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnalignedGenerationTail(t *testing.T) {
	parameters := json.RawMessage(`{"width":832,"height":480,"duration":4}`)
	manifest := json.RawMessage(`{"video":{"width":832,"height":480,"start_seconds":0,"duration_seconds":4.5,"avg_frame_rate":"24/1","frame_count":108,"pts_monotonic":true},"audio":{"present":true,"start_seconds":0,"duration_seconds":4.5}}`)

	if err := Validate("generation", parameters, manifest); err == nil {
		t.Fatal("expected unaligned frame count to fail")
	}
}
