/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package logging

import (
	"fmt"
	"io"
	"os"
	"strings"

	log "github.com/hashicorp/go-hclog"
)

type LogFormat int

const (
	UnspecifiedFormat LogFormat = iota
	StandardFormat
	JSONFormat
)

// Stringer implementation
func (l LogFormat) String() string {
	switch l {
	case UnspecifiedFormat:
		return "unspecified"
	case StandardFormat:
		return "standard"
	case JSONFormat:
		return "json"
	}

	// unreachable
	return "unknown"
}

// NewVaultLogger creates a new logger with the specified level and a Vault
// formatter
func NewVaultLogger(level log.Level) log.Logger {
	return NewVaultLoggerWithWriter(log.DefaultOutput, level)
}

// NewVaultLoggerWithWriter creates a new logger with the specified level and
// writer and a Vault formatter
func NewVaultLoggerWithWriter(w io.Writer, level log.Level) log.Logger {
	opts := &log.LoggerOptions{
		Level:             level,
		IndependentLevels: true,
		Output:            w,
		JSONFormat:        ParseEnvLogFormat() == JSONFormat,
	}
	return log.New(opts)
}

// ParseLogFormat parses the log format from the provided string.
func ParseLogFormat(format string) (LogFormat, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "":
		return UnspecifiedFormat, nil
	case "standard":
		return StandardFormat, nil
	case "json":
		return JSONFormat, nil
	default:
		return UnspecifiedFormat, fmt.Errorf("unknown log format: %s", format)
	}
}

// ParseEnvLogFormat parses the log format from an environment variable.
func ParseEnvLogFormat() LogFormat {
	logFormat := os.Getenv("ESS_LOG_FORMAT")
	switch strings.ToLower(logFormat) {
	case "json", "vault_json", "vault-json", "vaultjson":
		return JSONFormat
	case "standard":
		return StandardFormat
	default:
		return UnspecifiedFormat
	}
}
