package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
	"github.com/nchgroup/artifact-delivery-server/internal/pipeline"
	"github.com/nchgroup/artifact-delivery-server/internal/tlsconfig"
	"go.uber.org/zap"
)

const maxEncodedKeyHeaderBytes = 6000

type Server struct {
	config           *config.Config
	logger           *zap.Logger
	pipeline         artifactFetcher
	server           *http.Server
	tls              *tlsconfig.Runtime
	notFoundContents []byte
}

type artifactFetcher interface {
	FetchArtifactBundle(context.Context) (*pipeline.ArtifactBundle, error)
}

func New(cfg *config.Config, fetcher artifactFetcher, tlsRuntime *tlsconfig.Runtime, logger *zap.Logger) (*Server, error) {
	serverLogger := logger.Named("server")
	server := &Server{config: cfg, logger: serverLogger, pipeline: fetcher, tls: tlsRuntime}
	var indexContents []byte
	if cfg.IndexHTMLPath != "" {
		contents, err := os.ReadFile(cfg.IndexHTMLPath)
		if err != nil {
			return nil, err
		}
		indexContents = contents
		serverLogger.Info("Static index enabled", zap.String("path", cfg.IndexHTMLPath), zap.String("route", cfg.IndexHTMLRoute))
	}
	var notFoundContents []byte
	if cfg.NotFoundHTMLPath != "" {
		contents, err := os.ReadFile(cfg.NotFoundHTMLPath)
		if err != nil {
			return nil, err
		}
		notFoundContents = contents
		server.notFoundContents = contents
		serverLogger.Info("Custom 404 page enabled", zap.String("path", cfg.NotFoundHTMLPath))
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", cfg.ServerHeader)
		switch {
		case r.URL.Path == cfg.ServerPath:
			server.downloadHandler(w, r)
		case indexContents != nil && r.URL.Path == cfg.IndexHTMLRoute:
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(indexContents)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexContents)
		default:
			writeNotFound(w, notFoundContents)
		}
	})
	server.server = &http.Server{
		Addr:              net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort)),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		TLSConfig:         tlsRuntime.Config,
	}
	return server, nil
}

func (s *Server) Run() error {
	s.tls.StartChallengeServer(s.logger)
	s.logger.Info("Listening", zap.String("address", s.server.Addr), zap.String("path", s.config.ServerPath), zap.String("tls_mode", s.tls.Mode))
	if s.tls.Mode == "http" {
		return s.server.ListenAndServe()
	}
	return s.server.ListenAndServeTLS("", "")
}

func (s *Server) Shutdown(ctx context.Context) error {
	serverErr := s.server.Shutdown(ctx)
	challengeErr := s.tls.Shutdown(ctx)
	return stderrors.Join(serverErr, challengeErr)
}

func (s *Server) downloadHandler(w http.ResponseWriter, r *http.Request) {
	if !authorized(r.Header.Get("Authorization"), s.config.ServerToken) {
		s.logger.Warn("Rejected unauthenticated request", zap.String("remote", remoteHost(r.RemoteAddr)))
		writeNotFound(w, s.notFoundContents)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	s.logger.Info("Authenticated request", zap.String("remote", remoteHost(r.RemoteAddr)))
	bundle, err := s.pipeline.FetchArtifactBundle(r.Context())
	if err != nil {
		if stderrors.Is(err, context.Canceled) {
			s.logger.Info("Client disconnected while waiting for artifact", zap.String("remote", remoteHost(r.RemoteAddr)))
			return
		}
		s.writeAppError(w, err)
		return
	}
	defer bundle.Close()
	encodedKey := base64.StdEncoding.EncodeToString(bundle.DecryptionKey)
	if len(encodedKey) > maxEncodedKeyHeaderBytes {
		s.writeAppError(w, apperrors.New(http.StatusInternalServerError, "decryption key is too large to transport in a header"))
		return
	}
	w.Header().Set("Authorization", encodedKey)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(bundle.ArtifactSize, 10))
	w.Header().Set("Cache-Control", "no-store, private, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	written, copyErr := io.Copy(w, bundle.ArtifactFile)
	if copyErr != nil {
		s.logger.Error("Failed while serving artifact", zap.Error(copyErr), zap.Int64("bytes_written", written), zap.String("remote", remoteHost(r.RemoteAddr)))
		return
	}
	s.logger.Info("Artifact served", zap.String("release", bundle.ReleaseTag), zap.Int64("bytes", written), zap.String("remote", remoteHost(r.RemoteAddr)))
}

func authorized(header, expected string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	providedHash := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func (s *Server) writeAppError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "Internal server error"
	var applicationError *apperrors.Error
	if stderrors.As(err, &applicationError) {
		status = applicationError.Status
		if applicationError.Message != "" {
			message = applicationError.Message
		}
	}
	s.logger.Error("Request failed", zap.Int("status", status), zap.Error(err))
	writeJSONError(w, status, message)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func writeNotFound(w http.ResponseWriter, contents []byte) {
	if contents == nil {
		writeJSONError(w, http.StatusNotFound, "Not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(contents)
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}
