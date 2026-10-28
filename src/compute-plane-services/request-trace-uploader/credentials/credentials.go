// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package credentials reads backend credentials from the mounted secrets file.
//
// The file is the only source: values in the process environment are never
// read. Each backend names the keys it needs. The loader re-reads the file on
// a timer and whenever a backend reports an authentication failure, and keeps
// the last good values when a read fails, so a half-written file during a
// rotation never replaces working credentials.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DefaultFile            = "/var/secrets/secrets.json"
	DefaultRefreshInterval = 300 * time.Second
	MinRefreshInterval     = time.Second
	MaxRefreshInterval     = 24 * time.Hour
	// maxFileBytes bounds one read, so a wrong mount cannot exhaust memory.
	maxFileBytes = 1 << 20
)

// Config locates the secrets file and sets how often it is re-read.
type Config struct {
	File            string
	RefreshInterval time.Duration
}

// DefaultConfig returns the mount path NVCF uses and a five minute re-read.
func DefaultConfig() Config {
	return Config{File: DefaultFile, RefreshInterval: DefaultRefreshInterval}
}

// Validate reports the first invalid setting.
func (c Config) Validate() error {
	if !filepath.IsAbs(c.File) {
		return fmt.Errorf("credentials file must be an absolute path")
	}
	if c.RefreshInterval < MinRefreshInterval || c.RefreshInterval > MaxRefreshInterval {
		return fmt.Errorf("credentials refresh interval must be between %v and %v", MinRefreshInterval, MaxRefreshInterval)
	}
	return nil
}

// redacted replaces a credential value in every printed form.
const redacted = "[REDACTED]"

// Secret holds one credential value. Every formatting, logging and encoding
// path prints a fixed marker, so a value cannot reach a log line by accident.
type Secret struct{ value string }

// Reveal returns the raw value. Call it only where the value is handed to the
// remote service.
func (s Secret) Reveal() string { return s.value }

// Format implements fmt.Formatter for every verb, including %x and %#v.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// Loader holds the last good values of the keys one backend asked for.
type Loader struct {
	cfg  Config
	keys []string

	mu     sync.RWMutex
	values map[string]Secret

	newTicker func(time.Duration) (<-chan time.Time, func())
}

// Open reads keys from the secrets file. A missing file, or a missing, empty
// or non-string key, is a startup error.
func Open(cfg Config, keys ...string) (*Loader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("credentials: at least one key is required")
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key == "" {
			return nil, errors.New("credentials: key names must not be empty")
		}
		if seen[key] {
			return nil, fmt.Errorf("credentials: key %q requested twice", key)
		}
		seen[key] = true
	}
	loader := &Loader{cfg: cfg, keys: append([]string(nil), keys...), newTicker: systemTicker}
	values, err := loader.read()
	if err != nil {
		return nil, err
	}
	loader.values = values
	return loader, nil
}

// Get returns the last good value of key. It reports false only for a key
// that was not requested in Open.
func (l *Loader) Get(key string) (Secret, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	value, ok := l.values[key]
	return value, ok
}

// Reload re-reads the file. Backends call it after an authentication failure.
// On any read or parse failure the last good values stay in use and the error
// is returned; the error never contains a credential value.
func (l *Loader) Reload() error {
	values, err := l.read()
	if err != nil {
		return err
	}
	l.mu.Lock()
	changed := make([]string, 0)
	for _, key := range l.keys {
		if l.values[key] != values[key] {
			changed = append(changed, key)
		}
	}
	l.values = values
	l.mu.Unlock()
	if len(changed) > 0 {
		slog.Info("credentials changed in secrets file", "keys", changed)
	}
	return nil
}

func (l *Loader) read() (map[string]Secret, error) {
	file, err := os.Open(l.cfg.File)
	if err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("read secrets file: larger than %d bytes", maxFileBytes)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, parseError(err)
	}
	values := make(map[string]Secret, len(l.keys))
	for _, key := range l.keys {
		raw, ok := document[key]
		if !ok {
			return nil, fmt.Errorf("secrets file has no key %q", key)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("secrets file key %q is not a string", key)
		}
		if value == "" {
			return nil, fmt.Errorf("secrets file key %q is empty", key)
		}
		values[key] = Secret{value: value}
	}
	return values, nil
}

// parseError describes a JSON failure without the decoder's message, which can
// quote a character of the file and so part of a secret.
func parseError(err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Errorf("parse secrets file: invalid JSON at byte %d", syntax.Offset)
	}
	return errors.New("parse secrets file: not a JSON object")
}

func systemTicker(d time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(d)
	return ticker.C, ticker.Stop
}

// Run re-reads the file every RefreshInterval until ctx ends, so a secret
// rotated in place is used without a restart.
func (l *Loader) Run(ctx context.Context) {
	ticks, stop := l.newTicker(l.cfg.RefreshInterval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := l.Reload(); err != nil {
				slog.Warn("secrets file re-read failed, keeping last good credentials", "error", err)
			}
		}
	}
}
