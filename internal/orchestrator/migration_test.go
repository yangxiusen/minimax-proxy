package orchestrator

import (
	"context"
	"errors"
	"testing"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/store/sqlite"
)

type migrationStub struct {
	requestID string
	request   artifactservice.MigrationRequest
	calls     int
}

func (s *migrationStub) Migrate(_ context.Context, requestID string, request artifactservice.MigrationRequest) (sqlite.ArtifactLocation, error) {
	s.calls++
	s.requestID, s.request = requestID, request
	return sqlite.ArtifactLocation{ID: request.TargetLocationID, ArtifactID: request.ArtifactID, NodeID: request.TargetNodeID, NodeArtifactID: "target-artifact", State: "active", IsPrimary: true}, nil
}

func TestMigrationServiceKeepsNodeCredentialsBehindArtifactBoundary(t *testing.T) {
	stub := &migrationStub{}
	service := MigrationService{Artifacts: stub}
	result, err := service.Migrate(context.Background(), MigrationCommand{
		RequestID: "request-1", OperationID: "operation-1", ArtifactID: "logical-1",
		SourceNodeID: "source", TargetNodeID: "target", TargetLocationID: "location-1", Filename: "result.mp4",
	})
	if err != nil || result.NodeArtifactID != "target-artifact" || stub.calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, stub.calls, err)
	}
	if stub.requestID != "request-1" || stub.request.OperationID != "operation-1" || stub.request.TargetLocationID != "location-1" {
		t.Fatalf("request=%+v requestID=%q", stub.request, stub.requestID)
	}
}

func TestMigrationServiceHonorsCancelledContextBeforeRemoteIO(t *testing.T) {
	stub := &migrationStub{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (MigrationService{Artifacts: stub}).Migrate(ctx, MigrationCommand{OperationID: "operation", ArtifactID: "artifact", SourceNodeID: "source", TargetNodeID: "target", TargetLocationID: "location"})
	if !errors.Is(err, context.Canceled) || stub.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, stub.calls)
	}
}
