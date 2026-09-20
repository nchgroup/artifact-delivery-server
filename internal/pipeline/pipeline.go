package pipeline

import (
	"context"
	"os"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	"github.com/nchgroup/artifact-delivery-server/internal/gitea"
	"github.com/nchgroup/artifact-delivery-server/internal/release"
	"github.com/nchgroup/artifact-delivery-server/internal/workflow"
	"go.uber.org/zap"
)

type ArtifactBundle struct {
	DecryptionKey []byte
	ArtifactFile  *os.File
	ArtifactSize  int64
	ReleaseTag    string
}

func (b *ArtifactBundle) Close() {
	if b == nil || b.ArtifactFile == nil {
		return
	}
	name := b.ArtifactFile.Name()
	_ = b.ArtifactFile.Close()
	_ = os.Remove(name)
}

type Pipeline struct {
	workflow *workflow.Manager
	release  *release.Manager
	logger   *zap.Logger
}

func New(client *gitea.Client, cfg *config.Config, logger *zap.Logger) *Pipeline {
	return &Pipeline{
		workflow: workflow.New(client, cfg, logger),
		release:  release.New(client, cfg, logger),
		logger:   logger.Named("pipeline"),
	}
}

func (p *Pipeline) FetchArtifactBundle(ctx context.Context) (*ArtifactBundle, error) {
	run, err := p.workflow.TriggerAndWait(ctx)
	if err != nil {
		return nil, err
	}
	release, keyAsset, encryptedAsset, err := p.release.FindForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	p.logger.Debug("Required assets found", zap.String("key_asset", keyAsset.Name), zap.String("encrypted_asset", encryptedAsset.Name))
	key, err := p.release.DownloadKey(ctx, keyAsset)
	if err != nil {
		p.release.ReleaseClaim(release, keyAsset, encryptedAsset)
		return nil, err
	}
	artifact, size, err := p.release.DownloadArtifact(ctx, encryptedAsset)
	if err != nil {
		p.release.ReleaseClaim(release, keyAsset, encryptedAsset)
		return nil, err
	}
	return &ArtifactBundle{
		DecryptionKey: key,
		ArtifactFile:  artifact,
		ArtifactSize:  size,
		ReleaseTag:    release.TagName,
	}, nil
}
