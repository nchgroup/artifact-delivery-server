package config

import (
	"fmt"

	"github.com/alecthomas/kong"
)

const fullHelpDescription = `Gitea artifact delivery server

Examples:
  Basic server:

    artifact-delivery-server \
      --gitea-url https://gitea.example.com \
      --gitea-token "$GITEA_TOKEN" \
      --repository-owner admin \
      --repository-name rubeus \
      --workflow-name build.yml \
      --workflow-ref master \
      --decryption-key Rubeus.xor.key \
      --encrypted-file Rubeus.xor \
      --server-token "$SERVER_TOKEN"

  Restricted (guardrails) server with: IP policy, allowed host, headers, HMAC, and mTLS:

    artifact-delivery-server \
      --env-file .env.production \
      --allowed-client-ips "203.0.113.10,192.168.50.0/24,10.0.0.5-20" \
      --allowed-client-ips-file ./ips.txt \
      --allowed-host delivery.example.tld \
      --trusted-proxy-preset localhost \
      --client-header "X-Client-ID: $CLIENT_ID" \
      --client-header "X-Environment: production" \
      --server-header "X-Content-Type-Options: nosniff" \
      --server-header "X-Robots-Tag: noindex" \
      --server-banner "Microsoft-IIS/10.0" \
      --tls-cert-file ./server.crt \
      --tls-key-file ./server.key \
      --tls-client-ca-file ./client-ca.crt \
      --request-signing-key-file ./request-signing.key \
      --request-signing-window 60s

  Trusted-proxy examples:

    # Reverse proxy on the same machine (loopback only) with an additional client restriction.
    artifact-delivery-server --env-file .env.local \
      --trusted-proxy-preset localhost \
      --allowed-client-ips-file ./victim-ips.txt

    # Cloudflare in front of the origin.
    artifact-delivery-server --env-file .env.cloudflare \
      --trusted-proxy-preset cloudflare`

var completeHelpOptions = kong.HelpOptions{
	WrapUpperBound:  110,
	NoAppDescFormat: true,
	ValueFormatter:  kong.DefaultHelpValueFormatter,
}

type fullHelpFlag bool

func (fullHelpFlag) BeforeReset(ctx *kong.Context) error {
	if err := kong.DefaultHelpPrinter(completeHelpOptions, ctx); err != nil {
		return err
	}
	ctx.Kong.Exit(0)
	return nil
}

func briefHelpPrinter(_ kong.HelpOptions, ctx *kong.Context) error {
	_, err := fmt.Fprint(ctx.Stdout, `Gitea artifact delivery server

Basic usage:
  artifact-delivery-server \
    --gitea-url https://gitea.example.com \
    --gitea-token "$GITEA_TOKEN" \
    --repository-owner admin \
    --repository-name repository \
    --workflow-name build.yml \
    --workflow-ref master \
    --decryption-key Rubeus.xor.key \
    --encrypted-file Rubeus.xor \
    --server-token "$SERVER_TOKEN"

Basic options:
  --gitea-url          Base URL of the Gitea instance. Env: GITEA_URL.
  --gitea-token        Gitea API token. Env: GITEA_TOKEN.
  --repository-owner   Repository owner or organization. Env: REPOSITORY_OWNER.
  --repository-name    Repository name. Env: REPOSITORY_NAME.
  --workflow-name      Workflow file name, for example build.yml. Env: WORKFLOW_NAME.
  --workflow-ref       Branch or ref on which to run the workflow. Env: WORKFLOW_REF.
  --decryption-key     Release asset name of the decryption key. Env: DECRYPTION_KEY.
  --encrypted-file     Release asset name of the encrypted file. Env: ENCRYPTED_FILE.
  --server-token       Bearer token required by the download endpoint. Env: SERVER_TOKEN.

Run "artifact-delivery-server --help-full" for the complete option reference and examples.
`)
	return err
}
