package orchestrator

import (
	"context"
	"errors"

	artifactservice "minimax-h3-tc/internal/artifact"
	"minimax-h3-tc/internal/store/sqlite"
)

type ArtifactMigrator interface {
	Migrate(context.Context, string, artifactservice.MigrationRequest) (sqlite.ArtifactLocation, error)
}

type MigrationService struct {
	Artifacts ArtifactMigrator
}

type MigrationCommand struct {
	RequestID        string
	OperationID      string
	ArtifactID       string
	SourceNodeID     string
	TargetNodeID     string
	TargetLocationID string
	Filename         string
}

func (s MigrationService) Migrate(ctx context.Context, command MigrationCommand) (sqlite.ArtifactLocation, error) {
	if s.Artifacts == nil {
		return sqlite.ArtifactLocation{}, errors.New("跨节点产物迁移服务未配置")
	}
	if err := ctx.Err(); err != nil {
		return sqlite.ArtifactLocation{}, err
	}
	return s.Artifacts.Migrate(ctx, command.RequestID, artifactservice.MigrationRequest{
		OperationID: command.OperationID, ArtifactID: command.ArtifactID,
		SourceNodeID: command.SourceNodeID, TargetNodeID: command.TargetNodeID,
		TargetLocationID: command.TargetLocationID, Filename: command.Filename,
	})
}
