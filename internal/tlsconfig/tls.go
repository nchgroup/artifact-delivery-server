package tlsconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"
)

type Runtime struct {
	Config          *tls.Config
	Mode            string
	ChallengeServer *http.Server
	ChallengeListen net.Listener
}

func New(cfg *config.Config, logger *zap.Logger) (*Runtime, error) {
	base := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(cfg.ACMEDomains) > 0 {
		if err := os.MkdirAll(cfg.ACMECacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("create ACME cache directory: %w", err)
		}
		if err := os.Chmod(cfg.ACMECacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("protect ACME cache directory: %w", err)
		}
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cfg.ACMECacheDir),
			Email:      cfg.ACMEEmail,
			HostPolicy: autocert.HostWhitelist(cfg.ACMEDomains...),
		}
		tlsConfig := manager.TLSConfig()
		tlsConfig.MinVersion = tls.VersionTLS12
		if err := configureClientCertificates(tlsConfig, cfg.TLSClientCAFile); err != nil {
			return nil, err
		}
		runtime := &Runtime{Config: tlsConfig, Mode: "acme"}
		if cfg.ACMEHTTPAddress != "" {
			listener, err := net.Listen("tcp", cfg.ACMEHTTPAddress)
			if err != nil {
				return nil, fmt.Errorf("listen for ACME HTTP-01 challenges on %s: %w", cfg.ACMEHTTPAddress, err)
			}
			runtime.ChallengeListen = listener
			runtime.ChallengeServer = &http.Server{
				Handler:           manager.HTTPHandler(http.NotFoundHandler()),
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       30 * time.Second,
			}
			logger.Info("ACME HTTP-01 challenge listener ready", zap.String("address", cfg.ACMEHTTPAddress))
		}
		return runtime, nil
	}
	if cfg.AutoCert {
		certificate, certPEM, err := generateSelfSignedCertificate(cfg.AutoCertHosts)
		if err != nil {
			return nil, err
		}
		if err := writePublicCertificate(cfg.AutoCertOutput, certPEM); err != nil {
			return nil, err
		}
		base.Certificates = []tls.Certificate{certificate}
		if err := configureClientCertificates(base, cfg.TLSClientCAFile); err != nil {
			return nil, err
		}
		return &Runtime{Config: base, Mode: "auto-cert"}, nil
	}
	if cfg.TLSCertFile != "" {
		provider := &reloadingCertificate{certFile: cfg.TLSCertFile, keyFile: cfg.TLSKeyFile}
		if _, err := provider.GetCertificate(nil); err != nil {
			return nil, err
		}
		base.GetCertificate = provider.GetCertificate
		if err := configureClientCertificates(base, cfg.TLSClientCAFile); err != nil {
			return nil, err
		}
		return &Runtime{Config: base, Mode: "certificate-files"}, nil
	}
	return &Runtime{Mode: "http"}, nil
}

func configureClientCertificates(tlsConfig *tls.Config, caFile string) error {
	if caFile == "" {
		return nil
	}
	contents, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("read TLS client CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(contents) {
		return errors.New("TLS client CA file contains no valid PEM certificates")
	}
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	tlsConfig.ClientCAs = pool
	return nil
}

func generateSelfSignedCertificate(hosts []string) (tls.Certificate, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate auto-cert private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate auto-cert serial: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "artifact-delivery-server"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("create auto-cert certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("marshal auto-cert private key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load generated auto-cert certificate: %w", err)
	}
	return certificate, certPEM, nil
}

func writePublicCertificate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create auto-cert output directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".auto-cert-*.crt")
	if err != nil {
		return fmt.Errorf("create auto-cert output: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace auto-cert public certificate: %w", err)
	}
	return nil
}

type reloadingCertificate struct {
	certFile string
	keyFile  string
	mu       sync.Mutex
	certMod  time.Time
	keyMod   time.Time
	cert     *tls.Certificate
}

func (p *reloadingCertificate) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	certInfo, err := os.Stat(p.certFile)
	if err != nil {
		return nil, fmt.Errorf("stat TLS certificate: %w", err)
	}
	keyInfo, err := os.Stat(p.keyFile)
	if err != nil {
		return nil, fmt.Errorf("stat TLS private key: %w", err)
	}
	if p.cert != nil && certInfo.ModTime().Equal(p.certMod) && keyInfo.ModTime().Equal(p.keyMod) {
		return p.cert, nil
	}
	loaded, err := tls.LoadX509KeyPair(p.certFile, p.keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate and key: %w", err)
	}
	p.cert = &loaded
	p.certMod = certInfo.ModTime()
	p.keyMod = keyInfo.ModTime()
	return p.cert, nil
}

func (r *Runtime) StartChallengeServer(logger *zap.Logger) {
	if r.ChallengeServer == nil || r.ChallengeListen == nil {
		return
	}
	go func() {
		if err := r.ChallengeServer.Serve(r.ChallengeListen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("ACME HTTP-01 challenge server failed", zap.Error(err))
		}
	}()
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r.ChallengeServer == nil {
		return nil
	}
	return r.ChallengeServer.Shutdown(ctx)
}
