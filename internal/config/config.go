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
	"time"
	"unicode"

	"github.com/alecthomas/kong"
)

const (
	defaultMaxArtifactBytes = int64(100 * 1024 * 1024)
	defaultMaxKeyBytes      = int64(4096)
)

type Config struct {
	EnvFile string `name:"env-file" env:"ENV_FILE" type:"path" help:"Load configuration from this dotenv file instead of the automatic .env file; exported environment variables and command-line flags take precedence."`

	GiteaURL        string `name:"gitea-url" env:"GITEA_URL" required:"" help:"Base URL of the Gitea instance."`
	GiteaToken      string `name:"gitea-token" env:"GITEA_TOKEN" required:"" help:"Gitea API token."`
	RepositoryOwner string `name:"repository-owner" env:"REPOSITORY_OWNER" required:"" help:"Repository owner or organization."`
	RepositoryName  string `name:"repository-name" env:"REPOSITORY_NAME" required:"" help:"Repository name."`
	WorkflowName    string `name:"workflow-name" env:"WORKFLOW_NAME" required:"" help:"Workflow file name, for example build.yml."`
	WorkflowRef     string `name:"workflow-ref" env:"WORKFLOW_REF" default:"main" help:"Branch or ref on which to run the workflow."`

	ServerHost   string `name:"server-host" env:"SERVER_HOST" default:"0.0.0.0" help:"Interface on which the server listens."`
	ServerPort   int    `name:"server-port" env:"SERVER_PORT" default:"8080" help:"Port on which the server listens."`
	ServerPath   string `name:"server-path" env:"SERVER_PATH" default:"/download" help:"Path of the download endpoint."`
	ServerToken  string `name:"server-token" env:"SERVER_TOKEN" required:"" help:"Bearer token required by the download endpoint."`
	ServerHeader string `name:"server-header" env:"SERVER_HEADER" default:"Microsoft-IIS/10.0" help:"Value returned in the HTTP Server response header."`

	DecryptionKeyFile string `name:"decryption-key-file" env:"DECRYPTION_KEY_FILE" default:"decryption.key" help:"Release asset name of the decryption key."`
	EncryptedFile     string `name:"encrypted-file" env:"ENCRYPTED_FILE" default:"artifact.enc" help:"Release asset name of the encrypted file."`

	WorkflowTimeoutSeconds      int `name:"workflow-timeout" env:"WORKFLOW_TIMEOUT" default:"600" help:"Maximum seconds to wait for a workflow."`
	WorkflowPollIntervalSeconds int `name:"workflow-poll-interval" env:"WORKFLOW_POLL_INTERVAL" default:"5" help:"Seconds between workflow status checks."`
	GiteaRequestTimeoutSeconds  int `name:"gitea-request-timeout" env:"GITEA_REQUEST_TIMEOUT" default:"30" help:"Timeout in seconds for each Gitea request."`

	IndexHTMLPath    string `name:"index-html-path" env:"INDEX_HTML_PATH" type:"path" help:"Optional local index.html file to serve."`
	IndexHTMLRoute   string `name:"index-html-route" env:"INDEX_HTML_ROUTE" default:"/" help:"Route at which the optional index.html is served."`
	NotFoundHTMLPath string `name:"not-found-html-path" env:"NOT_FOUND_HTML_PATH" type:"path" help:"Optional local HTML file returned for unknown routes with status 404."`

	TLSCertFile string `name:"tls-cert-file" env:"TLS_CERT_FILE" type:"path" help:"TLS certificate chain file. Must be used with --tls-key-file; supports Certbot fullchain.pem."`
	TLSKeyFile  string `name:"tls-key-file" env:"TLS_KEY_FILE" type:"path" help:"TLS private key file. Must be used with --tls-cert-file; supports Certbot privkey.pem."`

	AutoCert       bool     `name:"auto-cert" env:"AUTO_CERT" help:"Generate a new self-signed certificate at every startup; mutually exclusive with certificate files and ACME."`
	AutoCertHosts  []string `name:"auto-cert-hosts" env:"AUTO_CERT_HOSTS" default:"localhost,127.0.0.1,::1" sep:"," help:"Comma-separated DNS names and IP addresses for auto-cert."`
	AutoCertOutput string   `name:"auto-cert-output" env:"AUTO_CERT_OUTPUT" default:"artifact-delivery-server.crt" type:"path" help:"File in which auto-cert writes only the public certificate; the private key remains in memory."`

	ACMEDomains     []string `name:"acme-domains" env:"ACME_DOMAINS" sep:"," help:"Comma-separated public domains for native ACME; mutually exclusive with certificate files and auto-cert."`
	ACMEEmail       string   `name:"acme-email" env:"ACME_EMAIL" help:"Contact email for the ACME account."`
	ACMECacheDir    string   `name:"acme-cache-dir" env:"ACME_CACHE_DIR" default:".artifact-delivery-server-acme" type:"path" help:"Private cache directory for ACME certificates and account keys."`
	ACMEHTTPAddress string   `name:"acme-http-address" env:"ACME_HTTP_ADDRESS" default:":80" help:"Address for the ACME HTTP-01 challenge server; empty disables HTTP-01."`
	ACMEAcceptTOS   bool     `name:"acme-accept-tos" env:"ACME_ACCEPT_TOS" help:"Accept the ACME certificate authority terms of service; required when --acme-domains is used."`

	MaxArtifactBytes int64 `name:"max-artifact-bytes" env:"MAX_ARTIFACT_BYTES" default:"104857600" help:"Maximum encrypted artifact size in bytes."`
	MaxKeyBytes      int64 `name:"max-key-bytes" env:"MAX_KEY_BYTES" default:"4096" help:"Maximum decryption key size in bytes."`

	LogFile       string `name:"log-file" env:"LOG_FILE" type:"path" help:"Optional file to which logs are appended."`
	LogFileFormat string `name:"log-file-format" env:"LOG_FILE_FORMAT" default:"text" enum:"text,json" help:"Format used in the log file. Available formats: text, json."`
	NoColor       bool   `name:"no-color" help:"Disable colors in console logs. Any non-empty $NO_COLOR environment variable also disables them."`
}

func Load(args []string) (*Config, error) {
	envFile, err := envFileFromArgs(args)
	if err != nil {
		return nil, err
	}
	cleanupEnvironment := func() {}
	if envFile != "" {
		cleanupEnvironment, err = loadEnvironmentFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("env-file: %w", err)
		}
	}
	defer cleanupEnvironment()

	cfg := &Config{}
	parser, err := kong.New(cfg,
		kong.Name("artifact-delivery-server"),
		kong.Description("Gitea artifact delivery server"),
		kong.UsageOnError(),
	)
	if err != nil {
		return nil, err
	}
	if _, err := parser.Parse(args); err != nil {
		return nil, err
	}
	if value, ok := os.LookupEnv("NO_COLOR"); ok && value != "" {
		cfg.NoColor = true
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

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
	if strings.ContainsAny(c.ServerHeader, "\r\n") {
		return errors.New("server-header cannot contain CR or LF characters")
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
	return nil
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

func (c *Config) WorkflowTimeout() time.Duration {
	return time.Duration(c.WorkflowTimeoutSeconds) * time.Second
}

func (c *Config) WorkflowPollInterval() time.Duration {
	return time.Duration(c.WorkflowPollIntervalSeconds) * time.Second
}

func (c *Config) GiteaRequestTimeout() time.Duration {
	return time.Duration(c.GiteaRequestTimeoutSeconds) * time.Second
}
