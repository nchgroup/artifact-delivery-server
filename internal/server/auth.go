package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
)

const (
	signatureHeader = "X-Signature"
	timestampHeader = "X-Timestamp"
	nonceHeader     = "X-Nonce"
)

func requiredHeadersMatch(header http.Header, required []config.Header) bool {
	for _, configuredHeader := range required {
		values := header.Values(configuredHeader.Name)
		if len(values) != 1 || !constantTimeEqual(values[0], configuredHeader.Value) {
			return false
		}
	}
	return true
}

func constantTimeEqual(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func authorized(header, expected string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	return constantTimeEqual(strings.TrimPrefix(header, "Bearer "), expected)
}

func authorizedHeaders(header http.Header, expected string) bool {
	values := header.Values("Authorization")
	return len(values) == 1 && authorized(values[0], expected)
}

func (s *Server) signedRequestAuthorized(r *http.Request, now time.Time) bool {
	if len(s.requestSigningKey) == 0 {
		return true
	}
	timestampText, ok := singleHeaderValue(r.Header, timestampHeader)
	if !ok {
		return false
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || strconv.FormatInt(timestamp, 10) != timestampText {
		return false
	}
	requestTime := time.Unix(timestamp, 0)
	delta := now.Sub(requestTime)
	if delta < -s.config.RequestSigningWindow || delta > s.config.RequestSigningWindow {
		return false
	}
	nonce, ok := singleHeaderValue(r.Header, nonceHeader)
	if !ok || !validNonce(nonce) {
		return false
	}
	signatureText, ok := singleHeaderValue(r.Header, signatureHeader)
	if !ok {
		return false
	}
	providedSignature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		return false
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	message := strings.Join([]string{r.Method, path, timestampText, nonce}, "\n")
	mac := hmac.New(sha256.New, s.requestSigningKey)
	_, _ = mac.Write([]byte(message))
	if !hmac.Equal(providedSignature, mac.Sum(nil)) {
		return false
	}

	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	for usedNonce, expiresAt := range s.usedNonces {
		if !expiresAt.After(now) {
			delete(s.usedNonces, usedNonce)
		}
	}
	if _, replayed := s.usedNonces[nonce]; replayed {
		return false
	}
	s.usedNonces[nonce] = requestTime.Add(s.config.RequestSigningWindow)
	return true
}

func singleHeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func validNonce(nonce string) bool {
	if len(nonce) < 16 || len(nonce) > 128 {
		return false
	}
	for index := 0; index < len(nonce); index++ {
		character := nonce[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}
