package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
)

const maxGiteaJSONBytes = int64(8 * 1024 * 1024)

type Client struct {
	baseURL *url.URL
	token   string
	client  *http.Client
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
	client := &http.Client{Timeout: timeout}
	g := &Client{baseURL: baseURL, token: token, client: client}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !g.sameOrigin(req.URL) {
			return fmt.Errorf("refusing cross-origin redirect to %s", req.URL.Redacted())
		}
		req.Header.Set("Authorization", "token "+g.token)
		return nil
	}
	return g, nil
}

func (g *Client) apiURL(parts ...string) *url.URL {
	segments := make([]string, 0, len(parts)+2)
	segments = append(segments, "api", "v1")
	for _, part := range parts {
		segments = append(segments, escapePathSegment(part))
	}
	apiURL := g.baseURL.JoinPath(segments...)
	apiURL.RawQuery = ""
	apiURL.Fragment = ""
	return apiURL
}

func escapePathSegment(segment string) string {
	switch segment {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return url.PathEscape(segment)
	}
}

func (g *Client) newAPIRequest(ctx context.Context, method string, body any, parts ...string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.apiURL(parts...).String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (g *Client) doAPI(req *http.Request, expected ...int) (*http.Response, error) {
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, apperrors.Wrap(http.StatusBadGateway, err, "failed to reach Gitea")
	}
	for _, status := range expected {
		if resp.StatusCode == status {
			return resp, nil
		}
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return nil, apperrors.New(http.StatusBadGateway, "Gitea returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
}

func decodeJSONResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxGiteaJSONBytes+1))
	if err := decoder.Decode(target); err != nil {
		return apperrors.Wrap(http.StatusBadGateway, err, "Gitea returned invalid JSON")
	}
	return nil
}

func (g *Client) TriggerWorkflowDispatch(ctx context.Context, owner, repo, workflow, ref string) error {
	req, err := g.newAPIRequest(ctx, http.MethodPost, map[string]string{"ref": ref}, "repos", owner, repo, "actions", "workflows", workflow, "dispatches")
	if err != nil {
		return err
	}
	resp, err := g.doAPI(req, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (g *Client) ListWorkflowRuns(ctx context.Context, owner, repo string) ([]WorkflowRun, error) {
	req, err := g.newAPIRequest(ctx, http.MethodGet, nil, "repos", owner, repo, "actions", "runs")
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	query.Set("limit", "50")
	req.URL.RawQuery = query.Encode()
	resp, err := g.doAPI(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := decodeJSONResponse(resp, &raw); err != nil {
		return nil, err
	}
	var wrapped struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.WorkflowRuns != nil {
		return wrapped.WorkflowRuns, nil
	}
	var runs []WorkflowRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, apperrors.Wrap(http.StatusBadGateway, err, "Gitea returned an unexpected workflow-runs response")
	}
	return runs, nil
}

func (g *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (*WorkflowRun, error) {
	req, err := g.newAPIRequest(ctx, http.MethodGet, nil, "repos", owner, repo, "actions", "runs", strconv.FormatInt(runID, 10))
	if err != nil {
		return nil, err
	}
	resp, err := g.doAPI(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var run WorkflowRun
	if err := decodeJSONResponse(resp, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (g *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	req, err := g.newAPIRequest(ctx, http.MethodGet, nil, "repos", owner, repo, "releases")
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	query.Set("limit", "50")
	req.URL.RawQuery = query.Encode()
	resp, err := g.doAPI(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var releases []Release
	if err := decodeJSONResponse(resp, &releases); err != nil {
		return nil, err
	}
	return releases, nil
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
