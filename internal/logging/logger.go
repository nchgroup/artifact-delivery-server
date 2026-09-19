package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	consoleTimeLayout = "06/01/02 15:04:05"
	fileTimeLayout    = "2006-01-02T15:04:05.000Z07:00"
)

type compactConsoleCore struct {
	output     zapcore.WriteSyncer
	level      zapcore.LevelEnabler
	color      bool
	timeLayout string
	fields     []zap.Field
}

func newCompactConsoleCore(output zapcore.WriteSyncer, level zapcore.LevelEnabler, color bool, timeLayout string) zapcore.Core {
	return &compactConsoleCore{output: output, level: level, color: color, timeLayout: timeLayout}
}

func (c *compactConsoleCore) Enabled(level zapcore.Level) bool { return c.level.Enabled(level) }

func (c *compactConsoleCore) With(fields []zap.Field) zapcore.Core {
	clone := *c
	clone.fields = append(append([]zap.Field(nil), c.fields...), fields...)
	return &clone
}

func (c *compactConsoleCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *compactConsoleCore) Write(entry zapcore.Entry, fields []zap.Field) error {
	component := entry.LoggerName
	if component == "" {
		component = "main"
	}
	var line strings.Builder
	line.WriteString(entry.Time.Format(c.timeLayout))
	line.WriteByte(' ')
	line.WriteString(compactLevel(entry.Level, c.color))
	line.WriteByte(' ')
	line.WriteString(fmt.Sprintf("%-9s", component))
	line.WriteByte(' ')
	line.WriteString(safeLogMessage(entry.Message))

	allFields := append(append([]zap.Field(nil), c.fields...), fields...)
	for _, field := range allFields {
		encoded := zapcore.NewMapObjectEncoder()
		field.AddTo(encoded)
		keys := make([]string, 0, len(encoded.Fields))
		for key := range encoded.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, include := compactFieldValue(encoded.Fields[key])
			if !include {
				continue
			}
			line.WriteByte(' ')
			line.WriteString(key)
			line.WriteByte('=')
			line.WriteString(value)
		}
	}
	line.WriteByte('\n')
	_, err := c.output.Write([]byte(line.String()))
	return err
}

func (c *compactConsoleCore) Sync() error { return c.output.Sync() }

func compactLevel(level zapcore.Level, color bool) string {
	value := fmt.Sprintf("%-5s", strings.ToUpper(level.String()))
	if !color {
		return value
	}
	code := "36"
	switch {
	case level >= zapcore.ErrorLevel:
		code = "31"
	case level == zapcore.WarnLevel:
		code = "33"
	case level == zapcore.InfoLevel:
		code = "92"
	case level <= zapcore.DebugLevel:
		code = "35"
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func compactFieldValue(value any) (string, bool) {
	if text, ok := value.(string); ok {
		if text == "" {
			return "", false
		}
		return safeLogText(text), true
	}
	if value == nil {
		return "null", true
	}
	if err, ok := value.(error); ok {
		return safeLogText(err.Error()), true
	}
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded), true
	}
	return safeLogText(fmt.Sprint(value)), true
}

func safeLogText(value string) string {
	if value != "" {
		bare := true
		for _, character := range value {
			if unicode.IsLetter(character) || unicode.IsNumber(character) || strings.ContainsRune("._:/@+-", character) {
				continue
			}
			bare = false
			break
		}
		if bare {
			return value
		}
	}
	return strconv.Quote(value)
}

func safeLogMessage(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}

func New(cfg *config.Config) (*zap.Logger, func(), error) {
	level := zapcore.DebugLevel

	cores := []zapcore.Core{
		newCompactConsoleCore(zapcore.Lock(os.Stderr), level, consoleSupportsColor() && !cfg.NoColor, consoleTimeLayout),
	}
	var logFile *os.File
	if cfg.LogFile != "" {
		var err error
		logFile, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		if err := logFile.Chmod(0o600); err != nil {
			_ = logFile.Close()
			return nil, nil, err
		}
		fileConfig := zap.NewProductionEncoderConfig()
		fileConfig.TimeKey = "time"
		fileConfig.NameKey = "component"
		fileConfig.MessageKey = "message"
		fileConfig.CallerKey = ""
		fileConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		fileConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		var encoder zapcore.Encoder
		if cfg.LogFileFormat == "json" {
			encoder = zapcore.NewJSONEncoder(fileConfig)
		} else {
			cores = append(cores, newCompactConsoleCore(zapcore.Lock(zapcore.AddSync(logFile)), level, false, fileTimeLayout))
			encoder = nil
		}
		if encoder != nil {
			cores = append(cores, zapcore.NewCore(encoder, zapcore.Lock(zapcore.AddSync(logFile)), level))
		}
	}

	logger := zap.New(zapcore.NewTee(cores...))
	cleanup := func() {
		_ = logger.Sync()
		if logFile != nil {
			_ = logFile.Close()
		}
	}
	return logger, cleanup, nil
}

func consoleSupportsColor() bool {
	if value, ok := os.LookupEnv("NO_COLOR"); ok && strings.TrimSpace(value) != "" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
