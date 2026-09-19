package release

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
	"github.com/nchgroup/artifact-delivery-server/internal/gitea"
	"github.com/nchgroup/artifact-delivery-server/internal/workflow"
	"go.uber.org/zap"
)

const (
	clockSkewTolerance      = 5 * time.Second
	releaseClaimRetention   = 24 * time.Hour
	maximumRememberedClaims = 4096
)

type Manager struct {
	client  *gitea.Client
	config  *config.Config
	logger  *zap.Logger
	claimMu sync.Mutex
	claimed map[string]time.Time
}

type releaseCandidate struct {
	release   gitea.Release
	keyAsset  gitea.Asset
	encAsset  gitea.Asset
	assetTime time.Time
	claimKey  string
}

func New(client *gitea.Client, cfg *config.Config, logger *zap.Logger) *Manager {
	return &Manager{client: client, config: cfg, logger: logger.Named("release"), claimed: make(map[string]time.Time)}
}

func (m *Manager) FindForRun(ctx context.Context, run *workflow.CompletedRun) (*gitea.Release, *gitea.Asset, *gitea.Asset, error) {
	releases, err := m.client.ListReleases(ctx, m.config.RepositoryOwner, m.config.RepositoryName)
	if err != nil {
		return nil, nil, nil, err
	}
	earliest := run.TriggerTime.Add(-clockSkewTolerance)
	var candidates []releaseCandidate
	for _, release := range releases {
		var keyAsset, encAsset *gitea.Asset
		keyCount, encCount := 0, 0
		for i := range release.Assets {
			switch release.Assets[i].Name {
			case m.config.DecryptionKeyFile:
				keyCount++
				copyAsset := release.Assets[i]
				keyAsset = &copyAsset
			case m.config.EncryptedFile:
				encCount++
				copyAsset := release.Assets[i]
				encAsset = &copyAsset
			}
		}
		if keyCount != 1 || encCount != 1 || keyAsset == nil || encAsset == nil || keyAsset.CreatedAt == nil || encAsset.CreatedAt == nil {
			continue
		}
		assetTime := *keyAsset.CreatedAt
		if encAsset.CreatedAt.After(assetTime) {
			assetTime = *encAsset.CreatedAt
		}
		if assetTime.Before(earliest) {
			continue
		}
		commitMatches := run.HeadSHA != "" && release.TargetCommitish == run.HeadSHA
		refMatches := run.Ref != "" && release.TargetCommitish == run.Ref
		if !commitMatches && !refMatches {
			continue
		}
		claimKey := fmt.Sprintf("%d:%d:%d:%d", release.ID, keyAsset.ID, encAsset.ID, assetTime.UnixNano())
		if m.isClaimed(claimKey) {
			continue
		}
		candidates = append(candidates, releaseCandidate{release: release, keyAsset: *keyAsset, encAsset: *encAsset, assetTime: assetTime, claimKey: claimKey})
	}
	if len(candidates) == 0 {
		return nil, nil, nil, apperrors.New(http.StatusNotFound, "no unambiguous release found for workflow run %d", run.ID)
	}
	if len(candidates) > 1 {
		return nil, nil, nil, apperrors.New(http.StatusInternalServerError, "multiple releases match workflow run %d; refusing an ambiguous artifact", run.ID)
	}
	selected := candidates[0]
	if !m.tryClaim(selected.claimKey) {
		return nil, nil, nil, apperrors.New(http.StatusInternalServerError, "release for workflow run %d was already claimed by another request", run.ID)
	}
	m.logger.Info("Release selected", zap.Int64("run_id", run.ID), zap.Int64("release_id", selected.release.ID), zap.String("tag", selected.release.TagName))
	return &selected.release, &selected.keyAsset, &selected.encAsset, nil
}

func (m *Manager) DownloadKey(ctx context.Context, asset *gitea.Asset) ([]byte, error) {
	if asset.BrowserDownloadURL == "" {
		return nil, apperrors.New(http.StatusNotFound, "decryption-key asset has no download URL")
	}
	data, err := m.client.DownloadBytes(ctx, asset.BrowserDownloadURL, asset.Size, m.config.MaxKeyBytes)
	if err != nil {
		return nil, err
	}
	m.logger.Debug("Downloaded decryption key", zap.String("asset", asset.Name), zap.Int("bytes", len(data)))
	return data, nil
}

func (m *Manager) DownloadArtifact(ctx context.Context, asset *gitea.Asset) (*os.File, int64, error) {
	if asset.BrowserDownloadURL == "" {
		return nil, 0, apperrors.New(http.StatusNotFound, "encrypted asset has no download URL")
	}
	temporary, err := os.CreateTemp("", "artifact-delivery-encrypted-*")
	if err != nil {
		return nil, 0, err
	}
	cleanup := func() {
		name := temporary.Name()
		_ = temporary.Close()
		_ = os.Remove(name)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return nil, 0, err
	}
	size, err := m.client.DownloadToFile(ctx, asset.BrowserDownloadURL, asset.Size, m.config.MaxArtifactBytes, temporary)
	if err != nil {
		cleanup()
		return nil, 0, err
	}
	m.logger.Debug("Downloaded encrypted artifact", zap.String("asset", asset.Name), zap.Int64("bytes", size))
	return temporary, size, nil
}

func (m *Manager) isClaimed(key string) bool {
	m.claimMu.Lock()
	defer m.claimMu.Unlock()
	m.pruneClaimsLocked(time.Now())
	_, exists := m.claimed[key]
	return exists
}

func (m *Manager) tryClaim(key string) bool {
	m.claimMu.Lock()
	defer m.claimMu.Unlock()
	now := time.Now()
	m.pruneClaimsLocked(now)
	if _, exists := m.claimed[key]; exists {
		return false
	}
	if len(m.claimed) >= maximumRememberedClaims {
		var oldestKey string
		var oldestTime time.Time
		for claimedKey, claimedAt := range m.claimed {
			if oldestKey == "" || claimedAt.Before(oldestTime) {
				oldestKey = claimedKey
				oldestTime = claimedAt
			}
		}
		delete(m.claimed, oldestKey)
	}
	m.claimed[key] = now
	return true
}

func (m *Manager) pruneClaimsLocked(now time.Time) {
	cutoff := now.Add(-releaseClaimRetention)
	for key, claimedAt := range m.claimed {
		if claimedAt.Before(cutoff) {
			delete(m.claimed, key)
		}
	}
}
