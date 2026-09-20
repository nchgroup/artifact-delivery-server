package proxytrust

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/netpolicy"
)

const (
	cloudflareIPURL = "https://api.cloudflare.com/client/v4/ips"
	cloudFrontIPURL = "https://ip-ranges.amazonaws.com/ip-ranges.json"
	fastlyIPURL     = "https://api.fastly.com/public-ip-list"

	requestTimeout       = 5 * time.Second
	maxProviderBodyBytes = int64(16 * 1024 * 1024)
)

type source struct {
	name   string
	url    string
	decode func([]byte) ([]string, error)
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
				return errors.New("provider redirect changed scheme or host")
			}
			return nil
		},
	}
}

func defaultSources() map[string]source {
	return map[string]source{
		"cloudflare": {
			name:   "cloudflare",
			url:    cloudflareIPURL,
			decode: decodeCloudflare,
		},
		"aws-cloudfront": {
			name:   "aws-cloudfront",
			url:    cloudFrontIPURL,
			decode: decodeCloudFront,
		},
		"fastly": {
			name:   "fastly",
			url:    fastlyIPURL,
			decode: decodeFastly,
		},
	}
}

func fetchSource(ctx context.Context, client *http.Client, provider source) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "artifact-delivery-server")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxProviderBodyBytes {
		return nil, fmt.Errorf("response exceeds the %d-byte limit", maxProviderBodyBytes)
	}
	entries, err := provider.decode(body)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("response contains no origin proxy ranges")
	}
	if _, err := netpolicy.Parse(entries, "", false); err != nil {
		return nil, fmt.Errorf("invalid provider range: %w", err)
	}
	return entries, nil
}

func decodeCloudflare(body []byte) ([]string, error) {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			IPv4 []string `json:"ipv4_cidrs"`
			IPv6 []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, errors.New("Cloudflare API reported an unsuccessful response")
	}
	return append(response.Result.IPv4, response.Result.IPv6...), nil
}

func decodeCloudFront(body []byte) ([]string, error) {
	var response struct {
		Prefixes []struct {
			Prefix  string `json:"ip_prefix"`
			Service string `json:"service"`
		} `json:"prefixes"`
		IPv6Prefixes []struct {
			Prefix  string `json:"ipv6_prefix"`
			Service string `json:"service"`
		} `json:"ipv6_prefixes"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	entries := []string{}
	for _, prefix := range response.Prefixes {
		if prefix.Service == "CLOUDFRONT_ORIGIN_FACING" {
			entries = append(entries, prefix.Prefix)
		}
	}
	for _, prefix := range response.IPv6Prefixes {
		if prefix.Service == "CLOUDFRONT_ORIGIN_FACING" {
			entries = append(entries, prefix.Prefix)
		}
	}
	return entries, nil
}

func decodeFastly(body []byte) ([]string, error) {
	var response struct {
		IPv4 []string `json:"addresses"`
		IPv6 []string `json:"ipv6_addresses"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return append(response.IPv4, response.IPv6...), nil
}
