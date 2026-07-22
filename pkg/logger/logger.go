package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	"github.com/gomessguii/logger"
	"gopkg.in/natefinch/lumberjack.v2"
)

var sensitiveLogPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)(\bBearer\s+)[A-Za-z0-9._~+/=-]+`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|token|password|passwd|secret|authorization)\b["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^,;\s]+)`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|token|password|passwd|secret|signature|sig|x-amz-signature|x-amz-credential|x-amz-security-token|googleaccessid)=)[^&#\s]+`),
		replacement: `${1}[REDACTED]`,
	},
	{
		pattern:     regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^:/@\s]+:)[^@/\s]+(@)`),
		replacement: `${1}[REDACTED]${2}`,
	},
}

type LoggerManager struct {
	config  *config.Config
	loggers map[string]*Logger
	mu      sync.RWMutex
}

type Logger struct {
	config     *config.Config
	instanceId string
	mu         sync.Mutex
	writer     *lumberjack.Logger
}

type LogEntry struct {
	Timestamp  time.Time       `json:"timestamp"`
	Level      string          `json:"level"`
	InstanceId string          `json:"instance_id"`
	Message    string          `json:"message"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

func NewLoggerManager(config *config.Config) *LoggerManager {
	// Garante que o diretório base de logs existe
	if err := os.MkdirAll(config.LogDirectory, 0755); err != nil {
		logger.LogError("Falha ao criar diretório base de logs: %v", err)
	}

	return &LoggerManager{
		config:  config,
		loggers: make(map[string]*Logger),
	}
}

func (lm *LoggerManager) GetLogger(instanceId string) *Logger {
	lm.mu.RLock()
	logger, exists := lm.loggers[instanceId]
	lm.mu.RUnlock()

	if exists {
		return logger
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()

	// Verificar novamente após obter o lock de escrita
	if logger, exists = lm.loggers[instanceId]; exists {
		return logger
	}

	// Criar novo logger para a instância
	logger = newLogger(instanceId, lm.config)
	lm.loggers[instanceId] = logger
	return logger
}

func newLogger(instanceId string, config *config.Config) *Logger {
	// Garante que o diretório existe
	logPath := filepath.Join(config.LogDirectory, instanceId)
	os.MkdirAll(logPath, 0755)

	logFile := filepath.Join(logPath, "instance.log")

	writer := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    config.LogMaxSize,
		MaxBackups: config.LogMaxBackups,
		MaxAge:     config.LogMaxAge,
		Compress:   config.LogCompress,
	}

	return &Logger{
		config:     config,
		instanceId: instanceId,
		writer:     writer,
	}
}

func (l *Logger) LogInfo(format string, args ...interface{}) {
	message := RedactSensitive(fmt.Sprintf(format, args...))
	l.log("INFO", message)
	logger.LogInfo("%s", message)
}

func (l *Logger) LogError(format string, args ...interface{}) {
	message := RedactSensitive(fmt.Sprintf(format, args...))
	l.log("ERROR", message)
	logger.LogError("%s", message)
}

func (l *Logger) LogWarn(format string, args ...interface{}) {
	message := RedactSensitive(fmt.Sprintf(format, args...))
	l.log("WARN", message)
	logger.LogWarn("%s", message)
}

func (l *Logger) LogDebug(format string, args ...interface{}) {
	message := RedactSensitive(fmt.Sprintf(format, args...))
	l.log("DEBUG", message)
	logger.LogDebug("%s", message)
}

// RedactSensitive removes common bearer credentials from structured and
// human-readable log messages. Callers must still avoid passing secrets to the
// logger: this is a defense-in-depth boundary, not a substitute for safe call
// sites.
func RedactSensitive(message string) string {
	for _, item := range sensitiveLogPatterns {
		message = item.pattern.ReplaceAllString(message, item.replacement)
	}
	return message
}

func (l *Logger) log(level string, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := LogEntry{
		Timestamp:  time.Now(),
		Level:      level,
		InstanceId: l.instanceId,
		Message:    message,
	}

	jsonEntry, err := json.Marshal(entry)
	if err != nil {
		logger.LogError("Failed to marshal log entry: %v", err)
		return
	}

	if _, err := l.writer.Write(append(jsonEntry, '\n')); err != nil {
		logger.LogError("Failed to write log: %v", err)
	}
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writer.Close()
}

// GetLogs retorna os logs da instância com filtros opcionais
func (l *Logger) GetLogs(startDate, endDate time.Time, level string, limit int) ([]LogEntry, error) {
	// Implementação movida para o service
	return nil, fmt.Errorf("método movido para instance_service")
}
