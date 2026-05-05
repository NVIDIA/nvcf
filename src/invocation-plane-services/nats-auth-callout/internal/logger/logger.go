/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logger

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/version"
)

// NewLogger creates a new zap logger based on the configuration
func NewLogger(cfg *config.LoggingConfig) (*zap.Logger, error) {
	// Parse log level
	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level '%s': %w", cfg.Level, err)
	}

	// Parse stacktrace level
	stacktraceLevel, err := parseLogLevel(cfg.StacktraceLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid stacktrace level '%s': %w", cfg.StacktraceLevel, err)
	}

	// Create encoder config
	encoderConfig := createEncoderConfig(cfg)

	// Create encoder
	encoder, err := createEncoder(cfg.Format, encoderConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create writer syncer
	writeSyncer := createWriteSyncer(cfg.Output)

	// Create core
	core := zapcore.NewCore(encoder, writeSyncer, level)

	// Add sampling if configured
	if cfg.Sampling.Initial > 0 && cfg.Sampling.Thereafter > 0 {
		core = zapcore.NewSamplerWithOptions(
			core,
			time.Second,             // tick interval
			cfg.Sampling.Initial,    // first
			cfg.Sampling.Thereafter, // thereafter
		)
	}

	// Create logger options
	var opts []zap.Option
	if cfg.Caller {
		opts = append(opts, zap.AddCaller())
	}
	if stacktraceLevel >= zapcore.DebugLevel {
		opts = append(opts, zap.AddStacktrace(stacktraceLevel))
	}
	if cfg.Development {
		opts = append(opts, zap.Development())
	}

	// Create logger
	logger := zap.New(core, opts...)

	// Add configured fields
	buildVersion := version.GetVersion()
	if cfg.Fields.ServiceName != "" || buildVersion != "" || cfg.Fields.Environment != "" {
		fields := make([]zap.Field, 0, 3)
		if cfg.Fields.ServiceName != "" {
			fields = append(fields, zap.String("service", cfg.Fields.ServiceName))
		}
		// Always use build-time version instead of config version
		if buildVersion != "" {
			fields = append(fields, zap.String("version", buildVersion))
		}
		if cfg.Fields.Environment != "" {
			fields = append(fields, zap.String("environment", cfg.Fields.Environment))
		}
		logger = logger.With(fields...)
	}

	return logger, nil
}

// parseLogLevel converts a string log level to zapcore.Level
func parseLogLevel(level string) (zapcore.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	case "panic":
		return zapcore.PanicLevel, nil
	case "fatal":
		return zapcore.FatalLevel, nil
	default:
		return zapcore.ErrorLevel, fmt.Errorf("unknown level: %s", level)
	}
}

// createEncoderConfig creates the encoder configuration based on logging config
func createEncoderConfig(cfg *config.LoggingConfig) zapcore.EncoderConfig {
	encoderConfig := zap.NewProductionEncoderConfig()

	if cfg.Development {
		encoderConfig = zap.NewDevelopmentEncoderConfig()
	}

	if cfg.DisableTimestamp {
		encoderConfig.TimeKey = ""
	} else {
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}

	// Configure level encoding based on format
	if cfg.Format == "console" || cfg.Format == "text" {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	} else {
		encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	}

	return encoderConfig
}

// createEncoder creates the appropriate encoder based on format
func createEncoder(format string, config zapcore.EncoderConfig) (zapcore.Encoder, error) {
	switch strings.ToLower(format) {
	case "json":
		return zapcore.NewJSONEncoder(config), nil
	case "console", "text":
		return zapcore.NewConsoleEncoder(config), nil
	default:
		return nil, fmt.Errorf("unknown log format: %s", format)
	}
}

// createWriteSyncer creates the appropriate write syncer based on output configuration
func createWriteSyncer(output string) zapcore.WriteSyncer {
	switch strings.ToLower(output) {
	case "stderr":
		return zapcore.AddSync(os.Stderr)
	default: // stdout is the default for containerized applications
		return zapcore.AddSync(os.Stdout)
	}
}
