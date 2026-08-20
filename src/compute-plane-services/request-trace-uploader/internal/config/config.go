// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package config loads the request-trace uploader's bounded runtime settings.
package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	EnvTraceDir             = "TRACE_DIR"
	EnvTraceFilePrefix      = "TRACE_FILE_PREFIX"
	EnvAuditFilePrefix      = "AUDIT_FILE_PREFIX"
	EnvSecretsFile          = "KRATOS_SECRETS_FILE"
	EnvStateDir             = "REQUEST_TRACE_UPLOADER_STATE_DIR"
	EnvQuarantineDir        = "REQUEST_TRACE_UPLOADER_QUARANTINE_DIR"
	EnvMetricsAddr          = "METRICS_ADDR"
	EnvUploadInterval       = "UPLOAD_INTERVAL_SECONDS"
	EnvStatusInterval       = "STATUS_INTERVAL_SECONDS"
	EnvStatusTimeout        = "STATUS_TIMEOUT_SECONDS"
	EnvAttemptTimeout       = "REQUEST_TRACE_UPLOADER_ATTEMPT_TIMEOUT"
	EnvOperationTimeout     = "REQUEST_TRACE_UPLOADER_OPERATION_TIMEOUT"
	EnvMaxRetries           = "REQUEST_TRACE_UPLOADER_MAX_RETRIES"
	EnvRetryInitialBackoff  = "REQUEST_TRACE_UPLOADER_RETRY_INITIAL_BACKOFF"
	EnvRetryMaximumBackoff  = "REQUEST_TRACE_UPLOADER_RETRY_MAX_BACKOFF"
	EnvRetryMultiplier      = "REQUEST_TRACE_UPLOADER_RETRY_MULTIPLIER"
	DefaultSecretsFile      = "/var/secrets/secrets.json"
	DefaultMetricsAddr      = ":8011"
	DefaultUploadInterval   = 30 * time.Second
	DefaultStatusInterval   = 5 * time.Second
	DefaultStatusTimeout    = 15 * time.Minute
	DefaultAttemptTimeout   = 30 * time.Second
	DefaultOperationTimeout = 90 * time.Second
	DefaultMaxRetries       = 2
	DefaultInitialBackoff   = 100 * time.Millisecond
	DefaultMaximumBackoff   = 15 * time.Second
	DefaultRetryMultiplier  = 2.0
)

// LookupFunc obtains one environment setting.
type LookupFunc func(string) (string, bool)

// Config is the request-trace uploader runtime configuration.
type Config struct {
	TraceDir        string
	TraceFilePrefix string
	AuditFilePrefix string
	SecretsFile     string
	StateDir        string
	QuarantineDir   string
	MetricsAddr     string
	UploadInterval  time.Duration
	StatusInterval  time.Duration
	StatusTimeout   time.Duration
	RetryPolicy     RetryPolicy
}

// RetryPolicy bounds each remote operation. The initial scaffold validates but
// does not yet invoke a remote upload client.
type RetryPolicy struct {
	AttemptTimeout   time.Duration
	OperationTimeout time.Duration
	MaxRetries       int
	InitialBackoff   time.Duration
	MaximumBackoff   time.Duration
	Multiplier       float64
}

// LoadFromEnv reads Config from the process environment.
func LoadFromEnv() (Config, []string, error) {
	return Load(os.LookupEnv)
}

// Load reads Config with lookup. Invalid optional policy values fall back to a
// default and add the setting name to warnings.
func Load(lookup LookupFunc) (Config, []string, error) {
	if lookup == nil {
		return Config{}, nil, fmt.Errorf("environment lookup is required")
	}

	traceDir, err := requiredAbsolute(lookup, EnvTraceDir)
	if err != nil {
		return Config{}, nil, err
	}
	tracePrefix, err := requiredName(lookup, EnvTraceFilePrefix)
	if err != nil {
		return Config{}, nil, err
	}
	auditPrefix, err := requiredName(lookup, EnvAuditFilePrefix)
	if err != nil {
		return Config{}, nil, err
	}
	if tracePrefix == auditPrefix {
		return Config{}, nil, fmt.Errorf("%s and %s must differ", EnvTraceFilePrefix, EnvAuditFilePrefix)
	}

	warnings := make([]string, 0)
	stateDir, err := optionalAbsolute(lookup, EnvStateDir, filepath.Join(traceDir, "request-trace-uploader-state"))
	if err != nil {
		return Config{}, nil, err
	}
	quarantineDir, err := optionalAbsolute(lookup, EnvQuarantineDir, filepath.Join(traceDir, "request-trace-uploader-quarantine"))
	if err != nil {
		return Config{}, nil, err
	}
	secretsFile := valueOrDefault(lookup, EnvSecretsFile, DefaultSecretsFile)
	metricsAddr := valueOrDefault(lookup, EnvMetricsAddr, DefaultMetricsAddr)
	if strings.TrimSpace(metricsAddr) == "" {
		return Config{}, nil, fmt.Errorf("%s must not be empty", EnvMetricsAddr)
	}

	uploadInterval := durationSeconds(lookup, EnvUploadInterval, DefaultUploadInterval, time.Second, 24*time.Hour, &warnings)
	statusInterval := durationSeconds(lookup, EnvStatusInterval, DefaultStatusInterval, time.Second, time.Hour, &warnings)
	statusTimeout := durationSeconds(lookup, EnvStatusTimeout, DefaultStatusTimeout, time.Second, 24*time.Hour, &warnings)
	if statusTimeout < statusInterval {
		statusTimeout = statusInterval
		warnings = append(warnings, EnvStatusTimeout)
	}
	attemptTimeout := duration(lookup, EnvAttemptTimeout, DefaultAttemptTimeout, time.Second, 90*time.Second, &warnings)
	operationTimeout := duration(lookup, EnvOperationTimeout, DefaultOperationTimeout, time.Second, 5*time.Minute, &warnings)
	if operationTimeout < attemptTimeout {
		operationTimeout = attemptTimeout
		warnings = append(warnings, EnvOperationTimeout)
	}
	maxRetries := integer(lookup, EnvMaxRetries, DefaultMaxRetries, 0, 10, &warnings)
	initialBackoff := duration(lookup, EnvRetryInitialBackoff, DefaultInitialBackoff, 10*time.Millisecond, 10*time.Second, &warnings)
	maximumBackoff := duration(lookup, EnvRetryMaximumBackoff, DefaultMaximumBackoff, 10*time.Millisecond, time.Minute, &warnings)
	if maximumBackoff < initialBackoff {
		maximumBackoff = initialBackoff
		warnings = append(warnings, EnvRetryMaximumBackoff)
	}
	multiplier := floatValue(lookup, EnvRetryMultiplier, DefaultRetryMultiplier, 1.1, 10.0, &warnings)

	return Config{
		TraceDir:        traceDir,
		TraceFilePrefix: tracePrefix,
		AuditFilePrefix: auditPrefix,
		SecretsFile:     strings.TrimSpace(secretsFile),
		StateDir:        stateDir,
		QuarantineDir:   quarantineDir,
		MetricsAddr:     strings.TrimSpace(metricsAddr),
		UploadInterval:  uploadInterval,
		StatusInterval:  statusInterval,
		StatusTimeout:   statusTimeout,
		RetryPolicy: RetryPolicy{
			AttemptTimeout:   attemptTimeout,
			OperationTimeout: operationTimeout,
			MaxRetries:       maxRetries,
			InitialBackoff:   initialBackoff,
			MaximumBackoff:   maximumBackoff,
			Multiplier:       multiplier,
		},
	}, warnings, nil
}

func requiredAbsolute(lookup LookupFunc, name string) (string, error) {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return validateAbsolute(name, value)
}

func optionalAbsolute(lookup LookupFunc, name, fallback string) (string, error) {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		value = fallback
	}
	return validateAbsolute(name, value)
}

func validateAbsolute(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	return filepath.Clean(value), nil
}

func requiredName(lookup LookupFunc, name string) (string, error) {
	value, ok := lookup(name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("%s must not contain a path separator", name)
	}
	return value, nil
}

func valueOrDefault(lookup LookupFunc, name, fallback string) string {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func durationSeconds(lookup LookupFunc, name string, fallback, minimum, maximum time.Duration, warnings *[]string) time.Duration {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		*warnings = append(*warnings, name)
		return fallback
	}
	if seconds < 0 || int64(seconds) > int64(maximum/time.Second) {
		*warnings = append(*warnings, name)
		return fallback
	}
	duration := time.Duration(seconds) * time.Second
	if duration < minimum || duration > maximum {
		*warnings = append(*warnings, name)
		return fallback
	}
	return duration
}

func duration(lookup LookupFunc, name string, fallback, minimum, maximum time.Duration, warnings *[]string) time.Duration {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		*warnings = append(*warnings, name)
		return fallback
	}
	return parsed
}

func integer(lookup LookupFunc, name string, fallback, minimum, maximum int, warnings *[]string) int {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		*warnings = append(*warnings, name)
		return fallback
	}
	return parsed
}

func floatValue(lookup LookupFunc, name string, fallback, minimum, maximum float64, warnings *[]string) float64 {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		*warnings = append(*warnings, name)
		return fallback
	}
	return parsed
}
