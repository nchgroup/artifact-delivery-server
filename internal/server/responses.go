package server

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strconv"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	apperrors "github.com/nchgroup/artifact-delivery-server/internal/errors"
	"go.uber.org/zap"
)

func setServerHeaders(header http.Header, cfg *config.Config) {
	header.Set("Server", cfg.ServerBanner)
	for _, configuredHeader := range cfg.ResponseHeaders() {
		header.Set(configuredHeader.Name, configuredHeader.Value)
	}
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
