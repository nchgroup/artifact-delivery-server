package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxHeaderFileBytes = int64(1024 * 1024)

type Header struct {
	Name  string
	Value string
}

func compileHeaders(option string, definitions []string, filePath string, clientHeaders bool) ([]Header, error) {
	allDefinitions := append([]string(nil), definitions...)
	if filePath != "" {
		fileDefinitions, err := readHeaderDefinitions(filePath)
		if err != nil {
			return nil, fmt.Errorf("%ss-file: %w", option, err)
		}
		allDefinitions = append(allDefinitions, fileDefinitions...)
	}

	compiled := make([]Header, 0, len(allDefinitions))
	seen := make(map[string]struct{}, len(allDefinitions))
	for _, definition := range allDefinitions {
		rawName, rawValue, found := strings.Cut(definition, ":")
		if !found {
			return nil, fmt.Errorf("%s %q must use 'Name: value' format", option, definition)
		}
		name := strings.TrimSpace(rawName)
		value := strings.TrimSpace(rawValue)
		if !validHeaderName(name) {
			return nil, fmt.Errorf("invalid %s name %q", option, rawName)
		}
		canonicalName := strings.ToLower(name)
		if _, exists := seen[canonicalName]; exists {
			return nil, fmt.Errorf("%s %q is configured more than once", option, name)
		}
		seen[canonicalName] = struct{}{}
		if forbiddenHeader(canonicalName, clientHeaders) {
			return nil, fmt.Errorf("%s %q is reserved and cannot be configured", option, name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%s value for %q cannot contain CR, LF, or NUL", option, name)
		}
		if clientHeaders && value == "" {
			return nil, fmt.Errorf("%s value for %q cannot be empty", option, name)
		}
		compiled = append(compiled, Header{Name: name, Value: value})
	}
	return compiled, nil
}

func readHeaderDefinitions(filePath string) ([]string, error) {
	info, err := os.Stat(filepath.Clean(filePath))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxHeaderFileBytes {
		return nil, fmt.Errorf("file exceeds the %d-byte limit", maxHeaderFileBytes)
	}
	file, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	definitions := []string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), int(maxHeaderFileBytes))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, ":") {
			return nil, fmt.Errorf("line %d must use 'Name: value' format", lineNumber)
		}
		definitions = append(definitions, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return definitions, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))) {
			return false
		}
	}
	return true
}

func forbiddenHeader(name string, request bool) bool {
	switch name {
	case "connection", "content-length", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	case "authorization":
		return true
	case "server", "content-type", "cache-control", "allow":
		return !request
	case "x-nonce", "x-signature", "x-timestamp":
		return request
	case "host":
		return request
	default:
		return false
	}
}
