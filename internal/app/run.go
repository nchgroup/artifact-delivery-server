package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	"github.com/nchgroup/artifact-delivery-server/internal/gitea"
	"github.com/nchgroup/artifact-delivery-server/internal/logging"
	"github.com/nchgroup/artifact-delivery-server/internal/pipeline"
	"github.com/nchgroup/artifact-delivery-server/internal/proxytrust"
	"github.com/nchgroup/artifact-delivery-server/internal/server"
	"github.com/nchgroup/artifact-delivery-server/internal/tlsconfig"
	"go.uber.org/zap"
)

func Run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	logger, cleanupLogger, err := logging.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	defer cleanupLogger()
	mainLogger := logger.Named("main")
	stopContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	proxyTrust, err := proxytrust.New(stopContext, cfg.TrustedProxies, cfg.TrustedProxyPresets)
	if err != nil {
		return fmt.Errorf("initialize trusted proxy presets: %w", err)
	}
	if proxyTrust.HasRemotePresets() {
		go proxyTrust.Run(stopContext, logger.Named("proxy-trust"))
	}

	mainLogger.Info("Starting Gitea artifact delivery server",
		zap.String("repository", cfg.RepositoryOwner+"/"+cfg.RepositoryName),
		zap.String("workflow", cfg.WorkflowName),
		zap.String("ref", cfg.WorkflowRef),
	)
	client, err := gitea.NewClient(cfg.GiteaURL, cfg.GiteaToken, cfg.GiteaRequestTimeout())
	if err != nil {
		return fmt.Errorf("initialize Gitea client: %w", err)
	}
	tlsRuntime, err := tlsconfig.New(cfg, logger.Named("tls"))
	if err != nil {
		return fmt.Errorf("initialize TLS: %w", err)
	}
	deliveryPipeline := pipeline.New(client, cfg, logger)
	httpServer, err := server.New(cfg, proxyTrust, deliveryPipeline, tlsRuntime, logger)
	if err != nil {
		return fmt.Errorf("initialize HTTP server: %w", err)
	}

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- httpServer.Run() }()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stopContext.Done():
		mainLogger.Info("Shutting down")
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		return nil
	}
}
