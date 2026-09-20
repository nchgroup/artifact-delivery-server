package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nchgroup/artifact-delivery-server/internal/netpolicy"
)

const maxSigningKeyBytes = int64(4096)

func (c *Config) Validate() error {
	parsed, err := url.Parse(c.GiteaURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("gitea-url must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("gitea-url cannot contain user information, a query, or a fragment")
	}
	c.GiteaURL = strings.TrimRight(c.GiteaURL, "/")

	if c.ServerPort < 1 || c.ServerPort > 65535 {
		return errors.New("server-port must be between 1 and 65535")
	}
	if err := validateAllowedHost(c.AllowedHost); err != nil {
		return err
	}
	if strings.ContainsAny(c.ServerBanner, "\r\n\x00") {
		return errors.New("server-banner cannot contain CR, LF, or NUL characters")
	}
	c.clientHeaderRules, err = compileHeaders("client-header", c.ClientHeaders, c.ClientHeadersFile, true)
	if err != nil {
		return err
	}
	c.responseHeaders, err = compileHeaders("server-header", c.ServerHeaders, c.ServerHeadersFile, false)
	if err != nil {
		return err
	}

	c.clientIPPolicy, err = netpolicy.Parse(c.AllowedClientIPs, c.AllowedClientIPsFile, true)
	if err != nil {
		return fmt.Errorf("allowed-client-ips: %w", err)
	}
	if _, err = netpolicy.Parse(c.TrustedProxies, "", false); err != nil {
		return fmt.Errorf("trusted-proxies: %w", err)
	}
	if c.RequestSigningWindow <= 0 {
		return errors.New("request-signing-window must be positive")
	}
	if c.RequestSigningKeyFile != "" {
		c.requestSigningKey, err = readLimitedSecret(c.RequestSigningKeyFile, maxSigningKeyBytes)
		if err != nil {
			return fmt.Errorf("request-signing-key-file: %w", err)
		}
	}
	for _, route := range []struct {
		name  string
		value string
	}{
		{name: "server-path", value: c.ServerPath},
		{name: "index-html-route", value: c.IndexHTMLRoute},
	} {
		if err := validateRoute(route.name, route.value); err != nil {
			return err
		}
	}
	if c.WorkflowTimeoutSeconds <= 0 || c.WorkflowPollIntervalSeconds <= 0 || c.GiteaRequestTimeoutSeconds <= 0 {
		return errors.New("workflow and request timeouts must be positive")
	}
	if c.MaxArtifactBytes <= 0 || c.MaxKeyBytes <= 0 {
		return errors.New("artifact and key size limits must be positive")
	}
	if c.MaxKeyBytes > 4500 {
		return errors.New("max-key-bytes cannot exceed 4500 because the key is transported in an HTTP header")
	}
	if c.IndexHTMLPath != "" {
		if err := requireRegularFile(c.IndexHTMLPath); err != nil {
			return fmt.Errorf("index-html-path: %w", err)
		}
		if c.IndexHTMLRoute == c.ServerPath {
			return errors.New("index-html-route cannot be the same as server-path")
		}
	}
	if c.NotFoundHTMLPath != "" {
		if err := requireRegularFile(c.NotFoundHTMLPath); err != nil {
			return fmt.Errorf("not-found-html-path: %w", err)
		}
	}

	manualTLS := c.TLSCertFile != "" || c.TLSKeyFile != ""
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("tls-cert-file and tls-key-file must be provided together")
	}
	if manualTLS {
		if err := requireRegularFile(c.TLSCertFile); err != nil {
			return fmt.Errorf("tls-cert-file: %w", err)
		}
		if err := requireRegularFile(c.TLSKeyFile); err != nil {
			return fmt.Errorf("tls-key-file: %w", err)
		}
	}
	tlsModes := 0
	if manualTLS {
		tlsModes++
	}
	if c.AutoCert {
		tlsModes++
		if len(c.AutoCertHosts) == 0 {
			return errors.New("auto-cert requires at least one auto-cert-host")
		}
		for index, host := range c.AutoCertHosts {
			c.AutoCertHosts[index] = strings.TrimSpace(host)
			if c.AutoCertHosts[index] == "" {
				return errors.New("auto-cert-hosts cannot contain empty names")
			}
		}
	}
	if len(c.ACMEDomains) > 0 {
		tlsModes++
		if !c.ACMEAcceptTOS {
			return errors.New("native ACME requires --acme-accept-tos")
		}
		for index, rawDomain := range c.ACMEDomains {
			domain := strings.TrimSpace(rawDomain)
			c.ACMEDomains[index] = domain
			if domain == "" || net.ParseIP(domain) != nil || strings.Contains(domain, ":") || strings.ContainsAny(domain, " \t\r\n") {
				return fmt.Errorf("invalid ACME domain %q", domain)
			}
		}
	}
	if tlsModes > 1 {
		return errors.New("manual TLS, auto-cert, and native ACME are mutually exclusive")
	}
	if c.TLSClientCAFile != "" {
		if tlsModes == 0 {
			return errors.New("tls-client-ca-file requires manual TLS, auto-cert, or ACME")
		}
		if err := requireRegularFile(c.TLSClientCAFile); err != nil {
			return fmt.Errorf("tls-client-ca-file: %w", err)
		}
	}
	return nil
}

func validateAllowedHost(host string) error {
	if host == "" {
		return nil
	}
	if strings.ContainsAny(host, "/?#\r\n\x00") || strings.Contains(host, ":") {
		return errors.New("allowed-host must be a hostname without scheme, path, or port")
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.Trim(host, ".") == "" {
		return errors.New("allowed-host must not be empty")
	}
	if net.ParseIP(host) != nil {
		return errors.New("allowed-host must be a DNS hostname, not an IP address")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid allowed-host label %q", label)
		}
		for _, character := range label {
			if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-') {
				return fmt.Errorf("invalid character in allowed-host %q", host)
			}
		}
	}
	return nil
}

func readLimitedSecret(filePath string, limit int64) ([]byte, error) {
	info, err := os.Stat(filepath.Clean(filePath))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds the %d-byte limit", limit)
	}
	contents, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, errors.New("file is empty")
	}
	return contents, nil
}

func validateRoute(name, route string) error {
	if !strings.HasPrefix(route, "/") {
		return fmt.Errorf("%s must start with '/'", name)
	}
	if strings.ContainsAny(route, "?#\\\r\n") {
		return fmt.Errorf("%s cannot contain a query, fragment, backslash, CR, or LF", name)
	}
	for _, character := range route {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return fmt.Errorf("%s cannot contain whitespace or control characters", name)
		}
	}
	if strings.Contains(route, "//") {
		return fmt.Errorf("%s must not contain repeated slashes", name)
	}
	cleaned := path.Clean(route)
	if route != "/" && strings.HasSuffix(route, "/") {
		cleaned += "/"
	}
	if cleaned != route {
		return fmt.Errorf("%s must be a canonical URL path", name)
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}
