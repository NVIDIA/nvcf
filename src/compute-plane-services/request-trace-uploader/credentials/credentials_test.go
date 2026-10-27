// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testIDKey     = "client_id"
	testSecretKey = "client_secret"
	testID        = "id-6b1f0c"
	testSecret    = "s3cr3t-value-9d2e"
)

func writeSecrets(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write secrets file: %v", err)
	}
}

func secretsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets.json")
}

func validSecrets() string {
	return `{"` + testIDKey + `":"` + testID + `","` + testSecretKey + `":"` + testSecret + `","unrelated":{"nested":true}}`
}

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.File != DefaultFile {
		t.Fatalf("File = %q, want %q", cfg.File, DefaultFile)
	}
	if cfg.RefreshInterval != DefaultRefreshInterval {
		t.Fatalf("RefreshInterval = %v, want %v", cfg.RefreshInterval, DefaultRefreshInterval)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty file", Config{File: "", RefreshInterval: time.Minute}},
		{"relative file", Config{File: "secrets.json", RefreshInterval: time.Minute}},
		{"zero interval", Config{File: "/var/secrets/secrets.json"}},
		{"interval below minimum", Config{File: "/var/secrets/secrets.json", RefreshInterval: MinRefreshInterval - time.Millisecond}},
		{"interval above maximum", Config{File: "/var/secrets/secrets.json", RefreshInterval: MaxRefreshInterval + time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestOpenReadsNamedKeysFromFile(t *testing.T) {
	path := secretsPath(t)
	writeSecrets(t, path, validSecrets())

	loader, err := Open(Config{File: path, RefreshInterval: time.Minute}, testIDKey, testSecretKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for key, want := range map[string]string{testIDKey: testID, testSecretKey: testSecret} {
		got, ok := loader.Get(key)
		if !ok {
			t.Fatalf("Get(%q) ok = false, want true", key)
		}
		if got.Reveal() != want {
			t.Fatalf("Get(%q) = %q, want %q", key, got.Reveal(), want)
		}
	}
	if _, ok := loader.Get("unrelated"); ok {
		t.Fatal("Get(unrequested key) ok = true, want false")
	}
}

func TestOpenFailsWithoutUsableFile(t *testing.T) {
	tests := []struct {
		name    string
		content *string
		wantErr string
	}{
		{name: "missing file", content: nil, wantErr: "read secrets file"},
		{name: "missing key", content: ptr(`{"` + testIDKey + `":"` + testID + `"}`), wantErr: testSecretKey},
		{name: "empty value", content: ptr(`{"` + testIDKey + `":"` + testID + `","` + testSecretKey + `":""}`), wantErr: testSecretKey},
		{name: "non-string value", content: ptr(`{"` + testIDKey + `":"` + testID + `","` + testSecretKey + `":42}`), wantErr: testSecretKey},
		{name: "not an object", content: ptr(`["` + testSecret + `"]`), wantErr: "parse secrets file"},
		{name: "truncated", content: ptr(`{"` + testIDKey + `":"` + testID + `","` + testSecretKey + `":"` + testSecret), wantErr: "parse secrets file"},
		{name: "empty file", content: ptr(""), wantErr: "parse secrets file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := secretsPath(t)
			if tt.content != nil {
				writeSecrets(t, path, *tt.content)
			}
			_, err := Open(Config{File: path, RefreshInterval: time.Minute}, testIDKey, testSecretKey)
			if err == nil {
				t.Fatal("Open() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Open() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpenIgnoresEnvironment(t *testing.T) {
	t.Setenv(testIDKey, testID)
	t.Setenv(testSecretKey, testSecret)

	_, err := Open(Config{File: secretsPath(t), RefreshInterval: time.Minute}, testIDKey, testSecretKey)
	if err == nil {
		t.Fatal("Open() error = nil with only env values set, want missing-file error")
	}
}

func TestOpenRejectsBadKeyLists(t *testing.T) {
	path := secretsPath(t)
	writeSecrets(t, path, validSecrets())
	tests := []struct {
		name string
		keys []string
	}{
		{"no keys", nil},
		{"empty key", []string{testIDKey, ""}},
		{"duplicate key", []string{testIDKey, testIDKey}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Open(Config{File: path, RefreshInterval: time.Minute}, tt.keys...); err == nil {
				t.Fatal("Open() error = nil, want error")
			}
		})
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	if _, err := Open(Config{File: "secrets.json", RefreshInterval: time.Minute}, testIDKey); err == nil {
		t.Fatal("Open() error = nil for a relative file, want error")
	}
}

func ptr(s string) *string { return &s }

func openValid(t *testing.T) (*Loader, string) {
	t.Helper()
	path := secretsPath(t)
	writeSecrets(t, path, validSecrets())
	loader, err := Open(Config{File: path, RefreshInterval: time.Minute}, testIDKey, testSecretKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return loader, path
}

func mustGet(t *testing.T, loader *Loader, key string) string {
	t.Helper()
	value, ok := loader.Get(key)
	if !ok {
		t.Fatalf("Get(%q) ok = false", key)
	}
	return value.Reveal()
}

func TestReloadPicksUpRotatedSecret(t *testing.T) {
	loader, path := openValid(t)
	rotated := "rotated-secret-41a7"
	writeSecrets(t, path, `{"`+testIDKey+`":"`+testID+`","`+testSecretKey+`":"`+rotated+`"}`)

	if err := loader.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := mustGet(t, loader, testSecretKey); got != rotated {
		t.Fatalf("secret after Reload = %q, want %q", got, rotated)
	}
}

func TestReloadKeepsLastGoodValuesOnBadRead(t *testing.T) {
	tests := []struct {
		name    string
		content *string
	}{
		{"file removed", nil},
		{"half written", ptr(`{"` + testIDKey + `":"new-id","` + testSecretKey + `":"new-sec`)},
		{"empty", ptr("")},
		{"key missing", ptr(`{"` + testIDKey + `":"new-id"}`)},
		{"key emptied", ptr(`{"` + testIDKey + `":"new-id","` + testSecretKey + `":""}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader, path := openValid(t)
			if tt.content == nil {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove secrets file: %v", err)
				}
			} else {
				writeSecrets(t, path, *tt.content)
			}

			if err := loader.Reload(); err == nil {
				t.Fatal("Reload() error = nil, want error")
			}
			if got := mustGet(t, loader, testIDKey); got != testID {
				t.Fatalf("id after failed Reload = %q, want last good %q", got, testID)
			}
			if got := mustGet(t, loader, testSecretKey); got != testSecret {
				t.Fatalf("secret after failed Reload = %q, want last good %q", got, testSecret)
			}
		})
	}
}
