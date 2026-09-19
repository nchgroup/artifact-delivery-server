package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/nchgroup/artifact-delivery-server/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

const (
	consoleTimeLayout = "06/01/02 15:04:05"
	fileTimeLayout    = "2006-01-02T15:04:05.000Z07:00"
)

var consoleBufferPool = buffer.NewPool()

type compactConsoleEncoder struct {
	zapcore.Encoder
	color      bool
	timeLayout string
}

func newConsoleEncoder(color bool, timeLayout string) zapcore.Encoder {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = ""
	encoderConfig.LevelKey = ""
	encoderConfig.NameKey = ""
	encoderConfig.MessageKey = ""
	encoderConfig.CallerKey = ""
	encoderConfig.StacktraceKey = ""
	return &compactConsoleEncoder{
		Encoder:    zapcore.NewConsoleEncoder(encoderConfig),
		color:      color,
		timeLayout: timeLayout,
	}
}

func (e *compactConsoleEncoder) Clone() zapcore.Encoder {
	return &compactConsoleEncoder{
		Encoder:    e.Encoder.Clone(),
		color:      e.color,
		timeLayout: e.timeLayout,
	}
}

func (e *compactConsoleEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	encodedFields, err := e.Encoder.EncodeEntry(entry, fields)
	if err != nil {
		return nil, err
	}
	defer encodedFields.Free()

	component := entry.LoggerName
	if component == "" {
		component = "main"
	}
	line := consoleBufferPool.Get()
	line.AppendString(entry.Time.Format(e.timeLayout))
	line.AppendByte(' ')
	line.AppendString(compactLevel(entry.Level, e.color))
	line.AppendByte(' ')
	line.AppendString(fmt.Sprintf("%-9s", component))
	line.AppendByte(' ')
	line.AppendString(safeLogMessage(entry.Message))

	if err := appendCompactFields(line, strings.TrimSpace(encodedFields.String())); err != nil {
		line.Free()
		return nil, err
	}
	line.AppendByte('\n')
	return line, nil
}

func appendCompactFields(line *buffer.Buffer, encoded string) error {
	if encoded == "" || encoded == "{}" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("unexpected console field encoding")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("unexpected console field key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		value, include, err := compactJSONValue(raw)
		if err != nil {
			return err
		}
		if include {
			line.AppendByte(' ')
			line.AppendString(key)
			line.AppendByte('=')
			line.AppendString(value)
		}
	}
	_, err = decoder.Token()
	return err
}

func compactJSONValue(raw json.RawMessage) (string, bool, error) {
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", false, err
		}
		if value == "" {
			return "", false, nil
		}
		return safeLogText(value), true, nil
	}
	return string(raw), true, nil
}

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

func safeLogText(value string) string {
	bare := value != ""
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

type singleLineCore struct {
	zapcore.Core
}

func newConsoleCore(output zapcore.WriteSyncer, level zapcore.LevelEnabler, color bool, timeLayout string) zapcore.Core {
	return singleLineCore{Core: zapcore.NewCore(newConsoleEncoder(color, timeLayout), output, level)}
}

func (c singleLineCore) With(fields []zapcore.Field) zapcore.Core {
	return singleLineCore{Core: c.Core.With(fields)}
}

func (c singleLineCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c singleLineCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	entry.Message = safeLogMessage(entry.Message)
	return c.Core.Write(entry, fields)
}

func New(cfg *config.Config) (*zap.Logger, func(), error) {
	level := zapcore.DebugLevel

	cores := []zapcore.Core{
		newConsoleCore(
			zapcore.Lock(os.Stderr),
			level,
			consoleSupportsColor() && !cfg.NoColor,
			consoleTimeLayout,
		),
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
		if cfg.LogFileFormat == "json" {
			cores = append(cores, zapcore.NewCore(
				zapcore.NewJSONEncoder(fileConfig),
				zapcore.Lock(zapcore.AddSync(logFile)),
				level,
			))
		} else {
			cores = append(cores, newConsoleCore(
				zapcore.Lock(zapcore.AddSync(logFile)),
				level,
				false,
				fileTimeLayout,
			))
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
