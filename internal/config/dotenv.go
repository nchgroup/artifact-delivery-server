package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
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

// loadEnvironmentFile adds only variables that are not already present in the
// process environment. The returned function removes the variables it added.
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

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxEnvFileBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		key, value, ok, err := parseEnvironmentLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if ok {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return values, nil
}

func parseEnvironmentLine(line string) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	if strings.HasPrefix(line, "export ") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	}

	separator := strings.IndexByte(line, '=')
	if separator < 0 {
		return "", "", false, errors.New("expected KEY=VALUE")
	}
	key := strings.TrimSpace(line[:separator])
	if !validEnvironmentKey(key) {
		return "", "", false, fmt.Errorf("invalid variable name %q", key)
	}
	value, err := parseEnvironmentValue(line[separator+1:])
	if err != nil {
		return "", "", false, fmt.Errorf("%s: %w", key, err)
	}
	return key, value, true, nil
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

func parseEnvironmentValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw[0] == '\'' || raw[0] == '"' {
		return parseQuotedEnvironmentValue(raw, raw[0])
	}
	for index, character := range raw {
		if character == '#' && (index == 0 || unicode.IsSpace(rune(raw[index-1]))) {
			raw = raw[:index]
			break
		}
	}
	return strings.TrimSpace(raw), nil
}

func parseQuotedEnvironmentValue(raw string, quote byte) (string, error) {
	var value strings.Builder
	escaped := false
	for index := 1; index < len(raw); index++ {
		character := raw[index]
		if quote == '"' && escaped {
			switch character {
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case '"', '\\':
				value.WriteByte(character)
			default:
				value.WriteByte('\\')
				value.WriteByte(character)
			}
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if character == quote {
			remainder := strings.TrimSpace(raw[index+1:])
			if remainder != "" && !strings.HasPrefix(remainder, "#") {
				return "", errors.New("unexpected content after quoted value")
			}
			return value.String(), nil
		}
		value.WriteByte(character)
	}
	return "", errors.New("unterminated quoted value")
}
