package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
	"github.com/nchgroup/artifact-delivery-server/internal/gitea"
	"go.uber.org/zap"
)

type CompletedRun struct {
	gitea.WorkflowRun
	TriggerTime time.Time
	Ref         string
}

type Manager struct {
	client     *gitea.Client
	config     *config.Config
	logger     *zap.Logger
	dispatch   chan struct{}
	claimedMu  sync.Mutex
	claimedIDs map[int64]struct{}
}

func New(client *gitea.Client, cfg *config.Config, logger *zap.Logger) *Manager {
	return &Manager{
		client: client, config: cfg, logger: logger.Named("workflow"),
		dispatch: make(chan struct{}, 1), claimedIDs: make(map[int64]struct{}),
	}
}

func (m *Manager) TriggerAndWait(ctx context.Context) (*CompletedRun, error) {
	select {
	case m.dispatch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, m.config.WorkflowTimeout())
	defer cancel()

	var run *gitea.WorkflowRun
	var triggerTime time.Time
	var err error
	func() {
		defer func() { <-m.dispatch }()
		run, triggerTime, err = m.dispatchAndIdentify(deadlineCtx)
	}()
	if err != nil {
		return nil, err
	}
	defer m.unclaim(run.ID)

	lastStatus, lastConclusion := "", ""
	for {
		fields := []zap.Field{zap.Int64("run_id", run.ID), zap.String("status", run.Status)}
		if run.Conclusion != "" {
			fields = append(fields, zap.String("conclusion", run.Conclusion))
		}
		if run.Status != lastStatus || run.Conclusion != lastConclusion {
			message := "Workflow status changed"
			if run.Status == "completed" {
				message = "Workflow run completed"
			}
			m.logger.Info(message, fields...)
			lastStatus, lastConclusion = run.Status, run.Conclusion
		} else {
			m.logger.Debug("Workflow status poll", fields...)
		}
		if run.Status == "completed" {
			break
		}
		if err := waitForPoll(deadlineCtx, m.config.WorkflowPollInterval()); err != nil {
			if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, apperrors.New(http.StatusGatewayTimeout, "workflow %q did not complete within %s", m.config.WorkflowName, m.config.WorkflowTimeout())
		}
		run, err = m.client.GetWorkflowRun(deadlineCtx, m.config.RepositoryOwner, m.config.RepositoryName, run.ID)
		if err != nil {
			return nil, err
		}
	}
	if run.Conclusion != "success" {
		return nil, apperrors.New(http.StatusInternalServerError, "workflow run %d finished with conclusion=%q", run.ID, run.Conclusion)
	}
	return &CompletedRun{WorkflowRun: *run, TriggerTime: triggerTime, Ref: m.config.WorkflowRef}, nil
}

func (m *Manager) dispatchAndIdentify(ctx context.Context) (*gitea.WorkflowRun, time.Time, error) {
	runs, err := m.client.ListWorkflowRuns(ctx, m.config.RepositoryOwner, m.config.RepositoryName)
	if err != nil {
		return nil, time.Time{}, err
	}
	baselineID := int64(0)
	for _, run := range runs {
		if m.matches(run) && run.ID > baselineID {
			baselineID = run.ID
		}
	}
	m.logger.Debug("Workflow baseline selected", zap.Int64("baseline_run_id", baselineID), zap.String("workflow", m.config.WorkflowName), zap.String("ref", m.config.WorkflowRef))

	triggerTime := time.Now().UTC()
	m.logger.Info("Triggering workflow", zap.String("workflow", m.config.WorkflowName), zap.String("ref", m.config.WorkflowRef))
	runID, err := m.client.TriggerWorkflowDispatch(ctx, m.config.RepositoryOwner, m.config.RepositoryName, m.config.WorkflowName, m.config.WorkflowRef)
	if err != nil {
		return nil, time.Time{}, err
	}
	if runID > 0 {
		run, err := m.client.GetWorkflowRun(ctx, m.config.RepositoryOwner, m.config.RepositoryName, runID)
		if err != nil {
			return nil, time.Time{}, err
		}
		m.claim(run.ID)
		m.logger.Info("Workflow run identified", zap.Int64("run_id", run.ID))
		return run, triggerTime, nil
	}

	for {
		runs, err = m.client.ListWorkflowRuns(ctx, m.config.RepositoryOwner, m.config.RepositoryName)
		if err != nil {
			return nil, time.Time{}, err
		}
		var candidate *gitea.WorkflowRun
		candidateCount := 0
		for i := range runs {
			run := &runs[i]
			if run.ID <= baselineID || !m.matches(*run) || m.isClaimed(run.ID) {
				continue
			}
			candidateCount++
			if candidate == nil || run.ID < candidate.ID {
				candidate = run
			}
		}
		if candidateCount > 1 {
			return nil, time.Time{}, apperrors.New(http.StatusInternalServerError, "multiple workflow runs appeared after dispatch; refusing ambiguous correlation")
		}
		if candidate != nil {
			m.claim(candidate.ID)
			m.logger.Info("Workflow run identified", zap.Int64("run_id", candidate.ID))
			return candidate, triggerTime, nil
		}
		if err := waitForPoll(ctx, m.config.WorkflowPollInterval()); err != nil {
			if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return nil, time.Time{}, err
			}
			return nil, time.Time{}, apperrors.New(http.StatusGatewayTimeout, "workflow %q did not appear within %s", m.config.WorkflowName, m.config.WorkflowTimeout())
		}
	}
}

func (m *Manager) matches(run gitea.WorkflowRun) bool {
	workflowPath := strings.SplitN(run.Path, "@", 2)[0]
	workflowMatches := workflowPath == m.config.WorkflowName || path.Base(workflowPath) == m.config.WorkflowName
	refMatches := run.HeadBranch == m.config.WorkflowRef || strings.HasSuffix(run.HeadBranch, "/"+m.config.WorkflowRef)
	eventMatches := run.Event == "" || run.Event == "workflow_dispatch"
	return workflowMatches && refMatches && eventMatches
}

func (m *Manager) isClaimed(id int64) bool {
	m.claimedMu.Lock()
	defer m.claimedMu.Unlock()
	_, exists := m.claimedIDs[id]
	return exists
}

func (m *Manager) claim(id int64) {
	m.claimedMu.Lock()
	m.claimedIDs[id] = struct{}{}
	m.claimedMu.Unlock()
}

func (m *Manager) unclaim(id int64) {
	m.claimedMu.Lock()
	delete(m.claimedIDs, id)
	m.claimedMu.Unlock()
}

func waitForPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait interrupted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
