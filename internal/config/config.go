package config

import (
	"fmt"
	"os"
	"time"

	"github.com/alecthomas/kong"
	"github.com/nchgroup/artifact-delivery-server/internal/netpolicy"
)

type Config struct {
	GeneralConfig  `embed:"" group:"configuration"`
	GiteaConfig    `embed:"" group:"gitea"`
	WorkflowConfig `embed:"" group:"workflow"`
	ServerConfig   `embed:"" group:"server"`
	NetworkConfig  `embed:"" group:"network"`
	AuthConfig     `embed:"" group:"authentication"`
	PagesConfig    `embed:"" group:"pages"`
	TLSConfig      `embed:"" group:"tls"`
	ACMEConfig     `embed:"" group:"acme"`
	LoggingConfig  `embed:"" group:"logging"`

	clientIPPolicy    *netpolicy.Policy
	requestSigningKey []byte
	clientHeaderRules []Header
	responseHeaders   []Header
}

type GeneralConfig struct {
	EnvFile  string       `name:"env-file" env:"ENV_FILE" type:"path" help:"Load configuration from this dotenv file instead of the automatic .env file; exported environment variables and command-line flags take precedence."`
	HelpFull fullHelpFlag `name:"help-full" help:"Show the complete option reference and examples."`
}

type GiteaConfig struct {
	GiteaURL                   string `name:"gitea-url" env:"GITEA_URL" required:"" help:"Base URL of the Gitea instance."`
	GiteaToken                 string `name:"gitea-token" env:"GITEA_TOKEN" required:"" help:"Gitea API token."`
	RepositoryOwner            string `name:"repository-owner" env:"REPOSITORY_OWNER" required:"" help:"Repository owner or organization."`
	RepositoryName             string `name:"repository-name" env:"REPOSITORY_NAME" required:"" help:"Repository name."`
	GiteaRequestTimeoutSeconds int    `name:"gitea-request-timeout" env:"GITEA_REQUEST_TIMEOUT" default:"30" help:"Timeout in seconds for each Gitea request."`
}

type WorkflowConfig struct {
	WorkflowName                string `name:"workflow-name" env:"WORKFLOW_NAME" required:"" help:"Workflow file name, for example build.yml."`
	WorkflowRef                 string `name:"workflow-ref" env:"WORKFLOW_REF" required:"" help:"Branch or ref on which to run the workflow."`
	WorkflowTimeoutSeconds      int    `name:"workflow-timeout" env:"WORKFLOW_TIMEOUT" default:"600" help:"Maximum seconds to wait for a workflow."`
	WorkflowPollIntervalSeconds int    `name:"workflow-poll-interval" env:"WORKFLOW_POLL_INTERVAL" default:"5" help:"Seconds between workflow status checks."`
	DecryptionKeyFile           string `name:"decryption-key" env:"DECRYPTION_KEY" required:"" help:"Release asset name of the decryption key."`
	EncryptedFile               string `name:"encrypted-file" env:"ENCRYPTED_FILE" required:"" help:"Release asset name of the encrypted file."`
	MaxArtifactBytes            int64  `name:"max-artifact-bytes" env:"MAX_ARTIFACT_BYTES" default:"104857600" help:"Maximum encrypted artifact size in bytes."`
	MaxKeyBytes                 int64  `name:"max-key-bytes" env:"MAX_KEY_BYTES" default:"4096" help:"Maximum decryption key size in bytes."`
}

type ServerConfig struct {
	ServerHost        string   `name:"server-host" env:"SERVER_HOST" default:"0.0.0.0" help:"Interface on which the server listens."`
	ServerPort        int      `name:"server-port" env:"SERVER_PORT" default:"8080" help:"Port on which the server listens."`
	ServerPath        string   `name:"server-path" env:"SERVER_PATH" default:"/download" help:"Path of the download endpoint."`
	AllowedHost       string   `name:"allowed-host" env:"ALLOWED_HOST" help:"Optional exact hostname required in the HTTP Host header, for example delivery.example.tld."`
	ServerBanner      string   `name:"server-banner" env:"SERVER_BANNER" default:"Microsoft-IIS/10.0" help:"Value returned in the standard HTTP Server response header."`
	ServerHeaders     []string `name:"server-header" sep:"none" placeholder:"NAME: VALUE" help:"Response header in 'Name: value' format; repeat to add more headers."`
	ServerHeadersFile string   `name:"server-headers-file" env:"SERVER_HEADERS_FILE" type:"path" help:"File containing response headers in 'Name: value' format, one per line."`
}

type NetworkConfig struct {
	AllowedClientIPs     []string `name:"allowed-client-ips" env:"ALLOWED_CLIENT_IPS" sep:"," placeholder:"ENTRY" help:"Allowed client IPs, CIDRs, or ranges; ranges may use 192.168.1.1-10 shorthand."`
	AllowedClientIPsFile string   `name:"allowed-client-ips-file" env:"ALLOWED_CLIENT_IPS_FILE" type:"path" help:"File containing allowed IPs, CIDRs, or ranges, one entry per line."`
	TrustedProxies       []string `name:"trusted-proxies" env:"TRUSTED_PROXIES" sep:"," placeholder:"IP-OR-CIDR" help:"Proxy addresses trusted to supply X-Forwarded-For."`
	TrustedProxyPresets  []string `name:"trusted-proxy-preset" env:"TRUSTED_PROXY_PRESET" enum:"localhost,cloudflare,aws-cloudfront,fastly" placeholder:"PRESET" help:"Preset: localhost, cloudflare, aws-cloudfront, or fastly; repeat to combine presets."`
}

type AuthConfig struct {
	ServerToken           string        `name:"server-token" env:"SERVER_TOKEN" required:"" help:"Bearer token required by the download endpoint."`
	ClientHeaders         []string      `name:"client-header" sep:"none" placeholder:"NAME: VALUE" help:"Required request header in 'Name: value' format; repeat to require more headers."`
	ClientHeadersFile     string        `name:"client-headers-file" env:"CLIENT_HEADERS_FILE" type:"path" help:"File containing required request headers in 'Name: value' format, one per line."`
	RequestSigningKeyFile string        `name:"request-signing-key-file" env:"REQUEST_SIGNING_KEY_FILE" type:"path" help:"Optional HMAC-SHA256 key file for signed requests with timestamp and nonce."`
	RequestSigningWindow  time.Duration `name:"request-signing-window" env:"REQUEST_SIGNING_WINDOW" default:"60s" help:"Maximum clock skew and age accepted for HMAC-signed requests."`
}

type PagesConfig struct {
	IndexHTMLPath    string `name:"index-html-path" env:"INDEX_HTML_PATH" type:"path" help:"Optional local index.html file to serve."`
	IndexHTMLRoute   string `name:"index-html-route" env:"INDEX_HTML_ROUTE" default:"/" help:"Route at which the optional index.html is served."`
	NotFoundHTMLPath string `name:"not-found-html-path" env:"NOT_FOUND_HTML_PATH" type:"path" help:"Optional local HTML file returned for unknown routes with status 404."`
}

type TLSConfig struct {
	TLSCertFile     string `name:"tls-cert-file" env:"TLS_CERT_FILE" type:"path" help:"TLS certificate chain file. Must be used with --tls-key-file; supports Certbot fullchain.pem."`
	TLSKeyFile      string `name:"tls-key-file" env:"TLS_KEY_FILE" type:"path" help:"TLS private key file. Must be used with --tls-cert-file; supports Certbot privkey.pem."`
	TLSClientCAFile string `name:"tls-client-ca-file" env:"TLS_CLIENT_CA_FILE" type:"path" help:"CA bundle used to require and verify mTLS client certificates."`

	AutoCert       bool     `name:"auto-cert" env:"AUTO_CERT" help:"Generate a new self-signed certificate at every startup; mutually exclusive with certificate files and ACME."`
	AutoCertHosts  []string `name:"auto-cert-hosts" env:"AUTO_CERT_HOSTS" default:"localhost,127.0.0.1,::1" sep:"," help:"Comma-separated DNS names and IP addresses for auto-cert."`
	AutoCertOutput string   `name:"auto-cert-output" env:"AUTO_CERT_OUTPUT" default:"artifact-delivery-server.crt" type:"path" help:"File in which auto-cert writes only the public certificate; the private key remains in memory."`
}

type ACMEConfig struct {
	ACMEDomains     []string `name:"acme-domains" env:"ACME_DOMAINS" sep:"," help:"Comma-separated public domains for native ACME; mutually exclusive with certificate files and auto-cert."`
	ACMEEmail       string   `name:"acme-email" env:"ACME_EMAIL" help:"Contact email for the ACME account."`
	ACMECacheDir    string   `name:"acme-cache-dir" env:"ACME_CACHE_DIR" default:".artifact-delivery-server-acme" type:"path" help:"Private cache directory for ACME certificates and account keys."`
	ACMEHTTPAddress string   `name:"acme-http-address" env:"ACME_HTTP_ADDRESS" default:":80" help:"Address for the ACME HTTP-01 challenge server; empty disables HTTP-01."`
	ACMEAcceptTOS   bool     `name:"acme-accept-tos" env:"ACME_ACCEPT_TOS" help:"Accept the ACME certificate authority terms of service; required when --acme-domains is used."`
}

type LoggingConfig struct {
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
		kong.Description(fullHelpDescription),
		kong.ExplicitGroups([]kong.Group{
			{Key: "configuration", Title: "Configuration:"},
			{Key: "gitea", Title: "Gitea:", Description: "Connection and source repository."},
			{Key: "workflow", Title: "Workflow and artifacts:", Description: "Workflow execution and generated release assets."},
			{Key: "server", Title: "HTTP server:", Description: "Listener and response headers."},
			{Key: "network", Title: "Client network policy:", Description: "IP allowlisting and trusted reverse proxies."},
			{Key: "authentication", Title: "Client authentication:", Description: "Bearer token, required headers, and optional request signing."},
			{Key: "pages", Title: "Static pages:"},
			{Key: "tls", Title: "TLS and client certificates:"},
			{Key: "acme", Title: "ACME:"},
			{Key: "logging", Title: "Logging:"},
		}),
		kong.Help(briefHelpPrinter),
		kong.ConfigureHelp(completeHelpOptions),
		kong.ShortUsageOnError(),
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

func (c *Config) WorkflowTimeout() time.Duration {
	return time.Duration(c.WorkflowTimeoutSeconds) * time.Second
}

func (c *Config) WorkflowPollInterval() time.Duration {
	return time.Duration(c.WorkflowPollIntervalSeconds) * time.Second
}

func (c *Config) GiteaRequestTimeout() time.Duration {
	return time.Duration(c.GiteaRequestTimeoutSeconds) * time.Second
}

func (c *Config) ClientIPPolicy() *netpolicy.Policy {
	return c.clientIPPolicy
}

func (c *Config) RequestSigningKey() []byte {
	return append([]byte(nil), c.requestSigningKey...)
}

func (c *Config) ClientHeaderRules() []Header {
	return append([]Header(nil), c.clientHeaderRules...)
}

func (c *Config) ResponseHeaders() []Header {
	return append([]Header(nil), c.responseHeaders...)
}
