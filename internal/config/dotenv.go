package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultEnvFile  = ".env"
	maxEnvFileBytes = 1024 * 1024
)

func envFileFromArgs(args []string) (string, error) {
	path, explicitlySelected := os.LookupEnv("ENV_FILE")
	if path == "" {
		explicitlySelected = false
	}
arguments:
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--env-file":
			if index+1 >= len(args) || args[index+1] == "" {
				return "", errors.New("--env-file requires a path")
			}
			index++
			path = args[index]
			explicitlySelected = true
		case strings.HasPrefix(argument, "--env-file="):
			path = strings.TrimPrefix(argument, "--env-file=")
			if path == "" {
				return "", errors.New("--env-file requires a path")
			}
			explicitlySelected = true
		case argument == "--":
			break arguments
		}
	}
	if explicitlySelected {
		return path, nil
	}

	if _, err := os.Stat(defaultEnvFile); err == nil {
		return defaultEnvFile, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect automatic %s file: %w", defaultEnvFile, err)
	}
	return "", nil
}

func loadEnvironmentFile(path string) (func(), error) {
	values, err := parseEnvironmentFile(path)
	if err != nil {
		return nil, err
	}

	loaded := make([]string, 0, len(values))
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			for _, loadedKey := range loaded {
				_ = os.Unsetenv(loadedKey)
			}
			return nil, fmt.Errorf("set %s: %w", key, err)
		}
		loaded = append(loaded, key)
	}

	return func() {
		for _, key := range loaded {
			_ = os.Unsetenv(key)
		}
	}, nil
}

func parseEnvironmentFile(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxEnvFileBytes {
		return nil, fmt.Errorf("file exceeds the %d-byte limit", maxEnvFileBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxEnvFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(contents) > maxEnvFileBytes {
		return nil, fmt.Errorf("file exceeds the %d-byte limit", maxEnvFileBytes)
	}

	values, err := godotenv.UnmarshalBytes(bytes.TrimPrefix(contents, []byte("\xef\xbb\xbf")))
	if err != nil {
		return nil, fmt.Errorf("parse file: %w", err)
	}
	for key := range values {
		if !validEnvironmentKey(key) {
			return nil, fmt.Errorf("invalid variable name %q", key)
		}
	}
	return values, nil
}

func validEnvironmentKey(key string) bool {
	if key == "" || !(key[0] == '_' || key[0] >= 'A' && key[0] <= 'Z' || key[0] >= 'a' && key[0] <= 'z') {
		return false
	}
	for index := 1; index < len(key); index++ {
		character := key[index]
		if character != '_' && !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}
