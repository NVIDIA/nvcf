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

package logchunkprocessor

import (
	"fmt"
	"unicode/utf8"
)

const (
	typeStr               = "logchunk"
	defaultMetadataPrefix = "log.chunk"
)

// Config controls oversized log body and attribute handling.
type Config struct {
	// MaxPayloadBytes is the maximum log body and attribute payload size before
	// the record is considered oversized. Zero disables the processor.
	MaxPayloadBytes int `mapstructure:"max_payload_bytes"`
	// MaxBodyBytes is a deprecated alias for MaxPayloadBytes.
	//
	// Deprecated: use MaxPayloadBytes / max_payload_bytes.
	MaxBodyBytes int `mapstructure:"max_body_bytes"`
	// DryRun records oversized log metrics and warnings without mutating logs.
	DryRun bool `mapstructure:"dry_run"`
	// MetadataPrefix is the attribute prefix used for chunk metadata.
	MetadataPrefix string `mapstructure:"metadata_prefix"`
}

func createDefaultConfig() *Config {
	return &Config{
		MaxPayloadBytes: 0,
		DryRun:          false,
		MetadataPrefix:  defaultMetadataPrefix,
	}
}

func (cfg *Config) Validate() error {
	maxPayloadBytes := cfg.effectiveMaxPayloadBytes()
	if maxPayloadBytes < 0 {
		return fmt.Errorf("max_payload_bytes must be greater than or equal to 0")
	}
	if maxPayloadBytes > 0 && maxPayloadBytes < utf8.UTFMax {
		return fmt.Errorf("max_payload_bytes must be 0 or at least %d to preserve UTF-8 chunk boundaries", utf8.UTFMax)
	}
	if cfg.MetadataPrefix == "" {
		return fmt.Errorf("metadata_prefix must not be empty")
	}
	return nil
}

func (cfg *Config) normalized() Config {
	normalized := *cfg
	normalized.MaxPayloadBytes = normalized.effectiveMaxPayloadBytes()
	if normalized.MetadataPrefix == "" {
		normalized.MetadataPrefix = defaultMetadataPrefix
	}
	return normalized
}

func (cfg Config) effectiveMaxPayloadBytes() int {
	if cfg.MaxPayloadBytes == 0 && cfg.MaxBodyBytes != 0 {
		return cfg.MaxBodyBytes
	}
	return cfg.MaxPayloadBytes
}

func (cfg Config) enabled() bool {
	return cfg.MaxPayloadBytes > 0
}

func (cfg Config) mode() string {
	if !cfg.enabled() {
		return "disabled"
	}
	if cfg.DryRun {
		return "dry_run"
	}
	return "chunk"
}
