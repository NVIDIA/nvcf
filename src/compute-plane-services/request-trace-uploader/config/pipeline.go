// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

const (
	// EnvAuditPrefix names a second segment family on the same spool, for a
	// producer configured to write one. Empty uploads one family.
	EnvAuditPrefix = "REQUEST_TRACE_UPLOADER_AUDIT_PREFIX"
	// EnvRedactMultimodal turns redaction of inline media on or off.
	EnvRedactMultimodal = "REQUEST_TRACE_UPLOADER_REDACT_MULTIMODAL"
	// EnvDrainTimeout bounds, in whole seconds, the shutdown drain after
	// SIGTERM. A deployment sets it to fit the pod's grace period.
	EnvDrainTimeout = "REQUEST_TRACE_UPLOADER_DRAIN_TIMEOUT_SECONDS"
	// EnvMaxJobAge is how long, in whole seconds, a submitted job may stay
	// unfinished before it is reported stranded.
	EnvMaxJobAge = "REQUEST_TRACE_UPLOADER_MAX_JOB_AGE_SECONDS"
	// EnvStatusInterval is how often, in whole seconds, the uploader polls
	// the status of every open job.
	EnvStatusInterval = "REQUEST_TRACE_UPLOADER_STATUS_INTERVAL_SECONDS"
	// DefaultPollInterval is the default for EnvStatusInterval.
	DefaultPollInterval = 30 * time.Second

	DefaultDrainTimeout = 60 * time.Second
	DefaultMaxJobAge    = 24 * time.Hour

	// settingPrefix starts every setting name this package reads, except
	// HEALTH_ADDR. LoadStrict rejects any other name with this prefix.
	settingPrefix = "REQUEST_TRACE_UPLOADER_"
)

// The names a deployment already sets for a sidecar uploader. They carry no
// backend meaning, so this package owns their aliases.
func init() {
	RegisterEnvAlias(EnvSourceDir, "TRACE_DIR")
	RegisterEnvAlias(EnvSegmentPrefix, "TRACE_FILE_PREFIX")
	RegisterEnvAlias(EnvScanInterval, "UPLOAD_INTERVAL_SECONDS")
	RegisterEnvAlias(EnvDrainTimeout, "SHUTDOWN_DRAIN_SECONDS")
}

// Prefixes lists the segment families to upload: the segment prefix, then
// the audit prefix when one is set.
func (c Config) Prefixes() []string {
	if c.AuditPrefix == "" {
		return []string{c.SegmentPrefix}
	}
	return []string{c.SegmentPrefix, c.AuditPrefix}
}

// LoadStrict reads Config the way a deployed uploader must: through the
// registered aliases, with every value that Load would replace by a default
// treated as an error, and with any unknown name that carries this
// package's prefix in environ treated as an error too, so a misspelled
// setting cannot be ignored. environ is the process environment in
// os.Environ form. Errors name settings, never their values.
func LoadStrict(lookup LookupFunc, environ []string) (Config, error) {
	aliased, err := WithAliases(lookup)
	if err != nil {
		return Config{}, err
	}
	cfg, warnings, err := Load(aliased)
	if err != nil {
		return Config{}, err
	}
	if len(warnings) > 0 {
		return Config{}, fmt.Errorf("invalid or incompatible values for %s", strings.Join(warnings, ", "))
	}
	if unknown := unknownSettings(environ); len(unknown) > 0 {
		return Config{}, fmt.Errorf("unknown settings %s", strings.Join(unknown, ", "))
	}
	return cfg, nil
}

func unknownSettings(environ []string) []string {
	known := knownSettings()
	aliasMu.RLock()
	defer aliasMu.RUnlock()
	var unknown []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, settingPrefix) || known[name] {
			continue
		}
		if _, isAlias := aliasOwner[name]; isAlias {
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return unknown
}

func auditPrefixValue(lookup LookupFunc, segmentPrefix string) (string, error) {
	value := strings.TrimSpace(valueOrDefault(lookup, EnvAuditPrefix, ""))
	if value == "" {
		return "", nil
	}
	if err := segment.ValidatePrefixes([]string{segmentPrefix, value}); err != nil {
		return "", fmt.Errorf("%s: %w", EnvAuditPrefix, err)
	}
	return value, nil
}

// strictBool parses a new boolean setting. Unlike the older settings, a bad
// value is an error and never a fallback.
func strictBool(lookup LookupFunc, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(valueOrDefault(lookup, name, ""))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

// strictSeconds parses a new whole-seconds setting within bounds. A bad
// value is an error and never a fallback.
func strictSeconds(lookup LookupFunc, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(valueOrDefault(lookup, name, ""))
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < int64(minimum/time.Second) || seconds > int64(maximum/time.Second) {
		return 0, fmt.Errorf("%s must be whole seconds from %d to %d", name, int64(minimum/time.Second), int64(maximum/time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}
