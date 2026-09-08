// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package config loads the request-trace uploader's bounded runtime settings.
package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	EnvSourceDir            = "REQUEST_TRACE_UPLOADER_SOURCE_DIR"
	EnvSegmentPrefix        = "REQUEST_TRACE_UPLOADER_SEGMENT_PREFIX"
	EnvBackend              = "REQUEST_TRACE_UPLOADER_BACKEND"
	EnvSecretsFile          = "REQUEST_TRACE_UPLOADER_SECRETS_FILE"
	EnvStateDir             = "REQUEST_TRACE_UPLOADER_STATE_DIR"
	EnvQuarantineDir        = "REQUEST_TRACE_UPLOADER_QUARANTINE_DIR"
	EnvHealthAddr           = "HEALTH_ADDR"
	EnvScanInterval         = "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL_SECONDS"
	EnvKratosStatusInterval = "REQUEST_TRACE_UPLOADER_KRATOS_STATUS_INTERVAL_SECONDS"
	EnvKratosStatusTimeout  = "REQUEST_TRACE_UPLOADER_KRATOS_STATUS_TIMEOUT_SECONDS"
	EnvAttemptTimeout       = "REQUEST_TRACE_UPLOADER_ATTEMPT_TIMEOUT"
	EnvOperationTimeout     = "REQUEST_TRACE_UPLOADER_OPERATION_TIMEOUT"
	EnvMaxRetries           = "REQUEST_TRACE_UPLOADER_MAX_RETRIES"
	EnvRetryInitialBackoff  = "REQUEST_TRACE_UPLOADER_RETRY_INITIAL_BACKOFF"
	EnvRetryMaximumBackoff  = "REQUEST_TRACE_UPLOADER_RETRY_MAX_BACKOFF"
	EnvRetryMultiplier      = "REQUEST_TRACE_UPLOADER_RETRY_MULTIPLIER"
	EnvObjectStoreBucket    = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_BUCKET"
	EnvObjectStoreRegion    = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_REGION"
	EnvObjectStoreEndpoint  = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_ENDPOINT"
	EnvObjectStoreKeyPrefix = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_KEY_PREFIX"
	EnvObjectStorePathStyle = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_PATH_STYLE"
	EnvObjectStoreDryRun    = "REQUEST_TRACE_UPLOADER_OBJECTSTORE_DRY_RUN"
	DefaultSecretsFile      = "/var/secrets/secrets.json"
	DefaultHealthAddr       = ":8011"
	DefaultSegmentPrefix    = "request-trace"
	DefaultScanInterval     = 30 * time.Second
	DefaultStatusInterval   = 5 * time.Second
	DefaultStatusTimeout    = 30 * time.Minute
	DefaultAttemptTimeout   = 30 * time.Second
	DefaultOperationTimeout = 90 * time.Second
	DefaultMaxRetries       = 2
	DefaultInitialBackoff   = 100 * time.Millisecond
	DefaultMaximumBackoff   = 15 * time.Second
	DefaultRetryMultiplier  = 2.0
)

// Backend selects the export destination. Core behavior derives from the
// backend's declared capabilities rather than from this value.
type Backend string

const (
	BackendObjectStore Backend = "objectstore"
	BackendKratos      Backend = "kratos"
	// BackendDebug reads and reports segments without exporting them. It
	// exists so the read path can be exercised against a real Dynamo without
	// credentials or a destination.
	BackendDebug Backend = "debug"
)

// LookupFunc obtains one environment setting.
type LookupFunc func(string) (string, bool)

// Config is the request-trace uploader runtime configuration.
//
// Dynamo v1.4.0 writes every record type to one rolling segment family, so the
// uploader scans a single prefix. Record classification comes from event_type
// on each record, which the parsing increment adds.
type Config struct {
	SourceDir     string
	SegmentPrefix string
	Backend       Backend
	SecretsFile   string
	StateDir      string
	QuarantineDir string
	HealthAddr    string
	ScanInterval  time.Duration
	RetryPolicy   RetryPolicy
	Kratos        KratosPolicy
	ObjectStore   ObjectStorePolicy
}

// ObjectStorePolicy configures the generic S3-compatible backend. Bucket and
// Region are validated by that backend's constructor, not here, because they
// only matter when Backend is objectstore.
type ObjectStorePolicy struct {
	Bucket string
	Region string
	// Endpoint overrides the AWS endpoint resolution for an S3-compatible
	// store that is not AWS. Empty uses the SDK default for Region. When set,
	// it must satisfy ValidObjectStoreEndpoint: an absolute https:// URL with
	// a host. A non-https or hostless endpoint would send credentials and
	// segment data in cleartext, or to nowhere.
	Endpoint string
	// KeyPrefix is joined with each segment's file name to form its object
	// key. Empty uploads to the bucket root.
	KeyPrefix string
	// PathStyle selects path-style bucket addressing, which most non-AWS
	// S3-compatible stores require.
	PathStyle bool
	// DryRun computes and logs the bucket, key, and size the backend would
	// upload, but never calls the store and never requires credentials. It
	// exists to exercise config, key computation, and hostname namespacing
	// without a destination, the same role debug plays for the read path.
	DryRun bool
}

// KratosPolicy bounds the asynchronous job polling that only the Kratos Bulk
// Upload backend performs. Object-store submission is synchronous and has no
// status poll.
type KratosPolicy struct {
	StatusInterval time.Duration
	StatusTimeout  time.Duration
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

	sourceDir, err := requiredAbsolute(lookup, EnvSourceDir)
	if err != nil {
		return Config{}, nil, err
	}
	segmentPrefix, err := optionalName(lookup, EnvSegmentPrefix, DefaultSegmentPrefix)
	if err != nil {
		return Config{}, nil, err
	}
	backend, err := backendValue(lookup, EnvBackend)
	if err != nil {
		return Config{}, nil, err
	}

	warnings := make([]string, 0)
	stateDir, err := optionalAbsolute(lookup, EnvStateDir, filepath.Join(sourceDir, "request-trace-uploader-state"))
	if err != nil {
		return Config{}, nil, err
	}
	quarantineDir, err := optionalAbsolute(lookup, EnvQuarantineDir, filepath.Join(sourceDir, "request-trace-uploader-quarantine"))
	if err != nil {
		return Config{}, nil, err
	}
	secretsFile := valueOrDefault(lookup, EnvSecretsFile, DefaultSecretsFile)
	healthAddr := valueOrDefault(lookup, EnvHealthAddr, DefaultHealthAddr)
	if strings.TrimSpace(healthAddr) == "" {
		return Config{}, nil, fmt.Errorf("%s must not be empty", EnvHealthAddr)
	}

	scanInterval := durationSeconds(lookup, EnvScanInterval, DefaultScanInterval, time.Second, 24*time.Hour, &warnings)
	statusInterval := durationSeconds(lookup, EnvKratosStatusInterval, DefaultStatusInterval, time.Second, time.Hour, &warnings)
	statusTimeout := durationSeconds(lookup, EnvKratosStatusTimeout, DefaultStatusTimeout, time.Second, 24*time.Hour, &warnings)
	if statusTimeout < statusInterval {
		statusTimeout = statusInterval
		warnings = append(warnings, EnvKratosStatusTimeout)
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

	objectStoreEndpoint := strings.TrimSpace(valueOrDefault(lookup, EnvObjectStoreEndpoint, ""))
	if !ValidObjectStoreEndpoint(objectStoreEndpoint) {
		return Config{}, nil, fmt.Errorf("%s must be an absolute https:// URL with a host; a non-https or hostless endpoint is invalid", EnvObjectStoreEndpoint)
	}
	objectStore := ObjectStorePolicy{
		Bucket:    strings.TrimSpace(valueOrDefault(lookup, EnvObjectStoreBucket, "")),
		Region:    strings.TrimSpace(valueOrDefault(lookup, EnvObjectStoreRegion, "")),
		Endpoint:  objectStoreEndpoint,
		KeyPrefix: strings.Trim(strings.TrimSpace(valueOrDefault(lookup, EnvObjectStoreKeyPrefix, "")), "/"),
		PathStyle: boolValue(lookup, EnvObjectStorePathStyle, false, &warnings),
		DryRun:    boolValue(lookup, EnvObjectStoreDryRun, false, &warnings),
	}

	return Config{
		SourceDir:     sourceDir,
		SegmentPrefix: segmentPrefix,
		Backend:       backend,
		SecretsFile:   strings.TrimSpace(secretsFile),
		StateDir:      stateDir,
		QuarantineDir: quarantineDir,
		HealthAddr:    strings.TrimSpace(healthAddr),
		ScanInterval:  scanInterval,
		Kratos: KratosPolicy{
			StatusInterval: statusInterval,
			StatusTimeout:  statusTimeout,
		},
		RetryPolicy: RetryPolicy{
			AttemptTimeout:   attemptTimeout,
			OperationTimeout: operationTimeout,
			MaxRetries:       maxRetries,
			InitialBackoff:   initialBackoff,
			MaximumBackoff:   maximumBackoff,
			Multiplier:       multiplier,
		},
		ObjectStore: objectStore,
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

func optionalName(lookup LookupFunc, name, fallback string) (string, error) {
	value, ok := lookup(name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		value = fallback
	}
	if strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("%s must not contain a path separator", name)
	}
	return value, nil
}

// ValidObjectStoreEndpoint reports whether endpoint is a safe value for
// ObjectStorePolicy.Endpoint: empty, meaning use the SDK default for Region,
// or an absolute https:// URL with a nonempty host. A bare scheme such as
// "https://" parses without error but has no host, so a prefix check alone
// would accept it; both Load and the objectstore backend's own constructor
// call this so the rule cannot be bypassed by constructing a Config directly.
func ValidObjectStoreEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host != ""
}

func backendValue(lookup LookupFunc, name string) (Backend, error) {
	value, ok := lookup(name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	switch Backend(value) {
	case BackendObjectStore, BackendKratos, BackendDebug:
		return Backend(value), nil
	default:
		return "", fmt.Errorf("%s must be one of %q, %q, or %q", name, BackendObjectStore, BackendKratos, BackendDebug)
	}
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

func boolValue(lookup LookupFunc, name string, fallback bool, warnings *[]string) bool {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
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
