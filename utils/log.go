package utils

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"os"
	"strings"
)

func InitializeLogger() (*zap.Logger, error) {
	// Definir o nível de log via variável de ambiente, default para Debug
	logLevelEnv := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level zapcore.Level
	switch logLevelEnv {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	case "dpanic":
		level = zap.DPanicLevel
	case "panic":
		level = zap.PanicLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.InfoLevel
	}

	// Configuração do encoder
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // Formato de tempo legível
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	// Detrminar o ambiente (development ou production)
	env := strings.ToLower(os.Getenv("ENV"))
	var encoder zapcore.Encoder
	if env == "prod" {
		encoder = zapcore.NewJSONEncoder(encoderConfig) // JSON para Produção
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig) // Console para desenvolvimento
	}

	lumberjackLogger := &lumberjack.Logger{
		Filename:   "app.log",
		MaxSize:    10, //Megabytes
		MaxBackups: 3,
		MaxAge:     28,   //Dias
		Compress:   true, //Compressão
	}

	var writeSyncer zapcore.WriteSyncer
	if env == "prod" {
		// Produção: Apenas no arquivo de log
		writeSyncer = zapcore.AddSync(lumberjackLogger)
	} else {
		// Desenvolvimento: Console e arquivo de log
		writeSyncer = zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), zapcore.AddSync(lumberjackLogger))
	}

	// Configuração do core com nível de log definido
	core := zapcore.NewCore(encoder, writeSyncer, level)

	// Construir o logger
	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	return logger, nil
}
