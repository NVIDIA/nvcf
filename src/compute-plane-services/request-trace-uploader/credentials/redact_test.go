// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package credentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSecretNeverPrintsItsValue(t *testing.T) {
	loader, _ := openValid(t)
	secret, _ := loader.Get(testSecretKey)
	holder := struct{ Secret Secret }{secret}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("credential", "secret", secret, "holder", holder)
	marshaled, err := json.Marshal(holder)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", secret),
		"%+v":  fmt.Sprintf("%+v", holder),
		"%#v":  fmt.Sprintf("%#v", holder),
		"%s":   fmt.Sprintf("%s", secret),
		"%q":   fmt.Sprintf("%q", secret),
		"%x":   fmt.Sprintf("%x", secret),
		"slog": logs.String(),
		"json": string(marshaled),
	}
	for name, output := range outputs {
		if strings.Contains(output, testSecret) {
			t.Errorf("%s output %q contains the secret", name, output)
		}
		if !strings.Contains(output, redacted) && name != "%x" {
			t.Errorf("%s output %q does not show the redaction marker", name, output)
		}
	}
}

func TestErrorsNeverContainSecretText(t *testing.T) {
	corrupt := []string{
		`{"` + testSecretKey + `": ` + testSecret + `}`,
		`{"` + testSecretKey + `": "` + testSecret + `"`,
		`{"` + testSecretKey + `": ["` + testSecret + `"]}`,
		`"` + testSecret + `"`,
		testSecret,
	}
	for _, content := range corrupt {
		path := secretsPath(t)
		writeSecrets(t, path, content)
		_, err := Open(Config{File: path, RefreshInterval: time.Minute}, testSecretKey)
		if err == nil {
			t.Fatalf("Open(%q) error = nil, want error", content)
		}
		for _, fragment := range []string{testSecret, string(testSecret[0]) + "'"} {
			if strings.Contains(err.Error(), fragment) {
				t.Errorf("Open(%q) error %q contains secret text %q", content, err, fragment)
			}
		}
	}
}
