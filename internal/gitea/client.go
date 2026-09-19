package gitea

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	giteasdk "gitea.dev/sdk"
	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
)

const maxGiteaJSONBytes = int64(8 * 1024 * 1024)

var errGiteaResponseTooLarge = errors.New("Gitea response exceeds the configured size limit")

type Client struct {
	baseURL *url.URL
	token   string
	client  *http.Client
	api     *giteasdk.Client
}

type WorkflowRun struct {
	ID          int64      `json:"id"`
	HeadSHA     string     `json:"head_sha"`
	HeadBranch  string     `json:"head_branch"`
	Path        string     `json:"path"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	Event       string     `json:"event"`
	CreatedAt   *time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type Release struct {
	ID              int64   `json:"id"`
	TagName         string  `json:"tag_name"`
	TargetCommitish string  `json:"target_commitish"`
	Assets          []Asset `json:"assets"`
}

type Asset struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Size               *int64     `json:"size"`
	CreatedAt          *time.Time `json:"created_at"`
	BrowserDownloadURL string     `json:"browser_download_url"`
}

func NewClient(rawBaseURL, token string, timeout time.Duration) (*Client, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, err
	}
	g := &Client{baseURL: baseURL, token: token}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !g.sameOrigin(req.URL) {
			return fmt.Errorf("refusing cross-origin redirect to %s", req.URL.Redacted())
		}
		req.Header.Set("Authorization", "token "+g.token)
		return nil
	}
	g.client = &http.Client{Timeout: timeout, CheckRedirect: checkRedirect}
	apiHTTPClient := &http.Client{
		Timeout:       timeout,
		CheckRedirect: checkRedirect,
		Transport: &responseSizeTransport{
			base:  http.DefaultTransport,
			limit: maxGiteaJSONBytes,
		},
	}
	g.api, err = giteasdk.NewClient(
		rawBaseURL,
		giteasdk.SetToken(token),
		giteasdk.SetHTTPClient(apiHTTPClient),
	)
	if err != nil {
		return nil, err
	}
	return g, nil
}

type responseSizeTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t *responseSizeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > t.limit {
		_ = response.Body.Close()
		return nil, errGiteaResponseTooLarge
	}
	response.Body = &limitedReadCloser{ReadCloser: response.Body, remaining: t.limit}
	return response, nil
}

type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(buffer []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:r.remaining]
		}
		read, err := r.ReadCloser.Read(buffer)
		r.remaining -= int64(read)
		return read, err
	}
	var probe [1]byte
	read, err := r.ReadCloser.Read(probe[:])
	if read > 0 {
		return 0, errGiteaResponseTooLarge
	}
	return 0, err
}

func apiError(response *giteasdk.Response, err error) error {
	if err == nil {
		return nil
	}
	if response != nil {
		return apperrors.Wrap(http.StatusBadGateway, err, "Gitea returned status %d", response.StatusCode)
	}
	return apperrors.Wrap(http.StatusBadGateway, err, "failed to reach Gitea")
}

func (g *Client) TriggerWorkflowDispatch(ctx context.Context, owner, repo, workflow, ref string) (int64, error) {
	details, response, err := g.api.Actions.DispatchRepoWorkflow(
		ctx,
		owner,
		repo,
		workflow,
		giteasdk.CreateActionsWorkflowDispatchOption{Ref: ref},
		true,
	)
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusNoContent) {
			return 0, nil
		}
		return 0, apiError(response, err)
	}
	if details == nil {
		return 0, nil
	}
	return details.WorkflowRunID, nil
}

func (g *Client) ListWorkflowRuns(ctx context.Context, owner, repo string) ([]WorkflowRun, error) {
	result, response, err := g.api.Actions.ListRepoRuns(ctx, owner, repo, giteasdk.ListRepoActionsRunsOptions{
		ListOptions: giteasdk.ListOptions{PageSize: 50},
	})
	if err != nil {
		return nil, apiError(response, err)
	}
	if result == nil {
		return nil, apperrors.New(http.StatusBadGateway, "Gitea returned an empty workflow-runs response")
	}
	runs := make([]WorkflowRun, 0, len(result.WorkflowRuns))
	for _, run := range result.WorkflowRuns {
		if run != nil {
			runs = append(runs, workflowRunFromSDK(run))
		}
	}
	return runs, nil
}

func (g *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*WorkflowRun, error) {
	run, response, err := g.api.Actions.GetRepoRun(ctx, owner, repo, runID)
	if err != nil {
		return nil, apiError(response, err)
	}
	if run == nil {
		return nil, apperrors.New(http.StatusBadGateway, "Gitea returned an empty workflow-run response")
	}
	mapped := workflowRunFromSDK(run)
	return &mapped, nil
}

func (g *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	result, response, err := g.api.Releases.ListReleases(ctx, owner, repo, giteasdk.ListReleasesOptions{
		ListOptions: giteasdk.ListOptions{PageSize: 50},
	})
	if err != nil {
		return nil, apiError(response, err)
	}
	releases := make([]Release, 0, len(result))
	for _, release := range result {
		if release != nil {
			releases = append(releases, releaseFromSDK(release))
		}
	}
	return releases, nil
}

func workflowRunFromSDK(run *giteasdk.ActionsWorkflowRun) WorkflowRun {
	return WorkflowRun{
		ID:          run.ID,
		HeadSHA:     run.HeadSha,
		HeadBranch:  run.HeadBranch,
		Path:        run.Path,
		Status:      run.Status,
		Conclusion:  run.Conclusion,
		Event:       run.Event,
		StartedAt:   optionalTime(run.StartedAt),
		CompletedAt: optionalTime(run.CompletedAt),
	}
}

func releaseFromSDK(release *giteasdk.Release) Release {
	assets := make([]Asset, 0, len(release.Attachments))
	for _, attachment := range release.Attachments {
		if attachment == nil {
			continue
		}
		var size *int64
		if attachment.Size > 0 {
			value := attachment.Size
			size = &value
		}
		assets = append(assets, Asset{
			ID:                 attachment.ID,
			Name:               attachment.Name,
			Size:               size,
			CreatedAt:          optionalTime(attachment.Created),
			BrowserDownloadURL: attachment.DownloadURL,
		})
	}
	return Release{
		ID:              release.ID,
		TagName:         release.TagName,
		TargetCommitish: release.Target,
		Assets:          assets,
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (g *Client) DownloadToFile(ctx context.Context, rawURL string, expectedSize *int64, limit int64, destination *os.File) (int64, error) {
	written, err := g.download(ctx, rawURL, expectedSize, limit, destination)
	if err != nil {
		return written, err
	}
	if err := destination.Sync(); err != nil {
		return written, err
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		return written, err
	}
	return written, nil
}

func (g *Client) download(ctx context.Context, rawURL string, expectedSize *int64, limit int64, destination io.Writer) (int64, error) {
	if limit <= 0 {
		return 0, apperrors.New(http.StatusInternalServerError, "asset size limit must be positive")
	}
	downloadURL, err := url.Parse(rawURL)
	if err != nil || !downloadURL.IsAbs() || downloadURL.User != nil {
		return 0, apperrors.New(http.StatusBadGateway, "Gitea returned an invalid asset download URL")
	}
	if !g.sameOrigin(downloadURL) {
		return 0, apperrors.New(http.StatusBadGateway, "refusing asset download from a different origin")
	}
	if expectedSize != nil {
		if *expectedSize < 0 {
			return 0, apperrors.New(http.StatusBadGateway, "Gitea returned an invalid negative asset size")
		}
		if *expectedSize > limit {
			return 0, apperrors.New(http.StatusInternalServerError, "asset exceeds the configured size limit")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, apperrors.Wrap(http.StatusBadGateway, err, "failed to download asset from Gitea")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, apperrors.New(http.StatusBadGateway, "asset download failed with status %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return 0, apperrors.New(http.StatusInternalServerError, "asset exceeds the configured size limit")
	}
	written, err := io.Copy(destination, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return written, apperrors.Wrap(http.StatusBadGateway, err, "failed while downloading asset")
	}
	if written > limit {
		return written, apperrors.New(http.StatusInternalServerError, "asset exceeds the configured size limit")
	}
	if written == 0 {
		return 0, apperrors.New(http.StatusInternalServerError, "asset downloaded as empty")
	}
	if expectedSize != nil && written != *expectedSize {
		return written, apperrors.New(http.StatusInternalServerError, "downloaded asset size %d does not match Gitea metadata %d", written, *expectedSize)
	}
	return written, nil
}

func (g *Client) DownloadBytes(ctx context.Context, rawURL string, expectedSize *int64, limit int64) ([]byte, error) {
	var destination bytes.Buffer
	destination.Grow(int(min(limit, 64*1024)))
	if _, err := g.download(ctx, rawURL, expectedSize, limit, &destination); err != nil {
		return nil, err
	}
	return destination.Bytes(), nil
}

func (g *Client) sameOrigin(candidate *url.URL) bool {
	return strings.EqualFold(candidate.Scheme, g.baseURL.Scheme) &&
		strings.EqualFold(candidate.Hostname(), g.baseURL.Hostname()) &&
		effectivePort(candidate) == effectivePort(g.baseURL)
}

func effectivePort(value *url.URL) string {
	if value.Port() != "" {
		return value.Port()
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}
