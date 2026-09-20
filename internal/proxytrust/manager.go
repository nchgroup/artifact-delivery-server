package proxytrust

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/netpolicy"
	"go.uber.org/zap"
)

const refreshInterval = 6 * time.Hour

type policySet struct {
	trusted    *netpolicy.Policy
	cloudflare *netpolicy.Policy
}

type sourceResult struct {
	name    string
	entries []string
	err     error
}

type Manager struct {
	client  *http.Client
	manual  []string
	presets []string
	sources map[string]source
	current atomic.Pointer[policySet]
}

func New(ctx context.Context, manual, presets []string) (*Manager, error) {
	manager := &Manager{
		client:  newHTTPClient(),
		manual:  append([]string(nil), manual...),
		presets: deduplicateStrings(presets),
		sources: defaultSources(),
	}
	if err := manager.refresh(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Policies() (*netpolicy.Policy, *netpolicy.Policy) {
	current := m.current.Load()
	if current == nil {
		return nil, nil
	}
	return current.trusted, current.cloudflare
}

func (m *Manager) HasRemotePresets() bool {
	for _, preset := range m.presets {
		if preset != "localhost" {
			return true
		}
	}
	return false
}

func (m *Manager) Run(ctx context.Context, logger *zap.Logger) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.refresh(ctx); err != nil {
				logger.Warn("Could not refresh trusted proxy presets; keeping the last valid policy", zap.Error(err))
				continue
			}
			logger.Info("Refreshed trusted proxy presets")
		}
	}
}

func (m *Manager) refresh(ctx context.Context) error {
	refreshContext, cancel := context.WithCancel(ctx)
	defer cancel()

	trustedEntries := append([]string(nil), m.manual...)
	remoteSources := make([]source, 0, len(m.presets))
	for _, preset := range m.presets {
		if preset == "localhost" {
			trustedEntries = append(trustedEntries, "127.0.0.0/8", "::1/128")
			continue
		}
		provider, exists := m.sources[preset]
		if !exists {
			return fmt.Errorf("unsupported trusted proxy preset %q", preset)
		}
		remoteSources = append(remoteSources, provider)
	}

	results := make(chan sourceResult, len(remoteSources))
	for _, provider := range remoteSources {
		go func(provider source) {
			entries, err := fetchSource(refreshContext, m.client, provider)
			results <- sourceResult{name: provider.name, entries: entries, err: err}
		}(provider)
	}

	entriesBySource := make(map[string][]string, len(remoteSources))
	for range remoteSources {
		result := <-results
		if result.err != nil {
			return fmt.Errorf("load %s trusted proxy ranges: %w", result.name, result.err)
		}
		entriesBySource[result.name] = result.entries
	}

	cloudflareEntries := []string{}
	for _, provider := range remoteSources {
		entries := entriesBySource[provider.name]
		trustedEntries = append(trustedEntries, entries...)
		if provider.name == "cloudflare" {
			cloudflareEntries = append(cloudflareEntries, entries...)
		}
	}

	trusted, err := netpolicy.Parse(trustedEntries, "", false)
	if err != nil {
		return fmt.Errorf("compile trusted proxy policy: %w", err)
	}
	cloudflare, err := netpolicy.Parse(cloudflareEntries, "", false)
	if err != nil {
		return fmt.Errorf("compile Cloudflare proxy policy: %w", err)
	}
	m.current.Store(&policySet{trusted: trusted, cloudflare: cloudflare})
	return nil
}

func deduplicateStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
