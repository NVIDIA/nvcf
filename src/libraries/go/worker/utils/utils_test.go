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

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// ------------------------------------------------------------------------

// Test GetDirectorySize function
func TestGetDirectorySize(t *testing.T) {
	// Create test directories and files
	testDir := t.TempDir()
	var totalSize int64
	err := testutils.CreateFileBySize(filepath.Join(testDir, "file0"), 50)
	if err != nil {
		t.Fatal(err)
	}
	totalSize += 50

	for i := 1; i <= 2; i++ {
		subDir := filepath.Join(testDir, fmt.Sprintf("test%d", i))
		err = os.Mkdir(subDir, os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}

		testFile := filepath.Join(subDir, fmt.Sprintf("file%d", i))
		testFileSize := int64(i * 100)
		totalSize += testFileSize
		err = testutils.CreateFileBySize(testFile, testFileSize)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Get dir size and compare resutls
	dirSize, err := GetDirectorySize(testDir)
	if err != nil {
		t.Fatal(err)
	}

	if dirSize != totalSize {
		t.Fatalf("Get dir size = %d, want = %d\n", dirSize, totalSize)
	}
}

// ------------------------------------------------------------------------

// Test SanitizePath function
func TestSanitizePath(t *testing.T) {
	testPath := []string{
		"",
		"./file",
		"../file",
		"./../some/./path/to/../something",
		"../../../etc/password",
		"/../etc/password",
		"/etc/secrets/password",
	}

	expectedPath := []string{
		"",
		"file",
		"file",
		"some/path/something",
		"etc/password",
		"/etc/password",
		"/etc/secrets/password",
	}

	for i, path := range testPath {
		result := SanitizePath(path)
		if result != expectedPath[i] {
			t.Fatalf("Get path: %s, expected path: %s", result, expectedPath[i])
		}
	}

}

func TestCreateDirectory(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any
	undo := zap.ReplaceGlobals(logger)
	defer undo()

	t.Run("successfully create directory that does not exist", func(t *testing.T) {
		dir := t.TempDir()
		defer os.RemoveAll(dir)

		newDir := filepath.Join(dir, "newDir")
		err := CreateDirectory(newDir, 0755)
		if err != nil {
			t.Fatalf("expected no error, but got %v", err)
		}

		_, err = os.Stat(newDir)
		if os.IsNotExist(err) {
			t.Fatalf("expected directory to be created, but it does not exist")
		}
	})

	t.Run("succeed if the directory exists and permissions can be changed", func(t *testing.T) {
		dir := t.TempDir()
		defer os.RemoveAll(dir)

		newDir := filepath.Join(dir, "newDir")
		if err := os.Mkdir(newDir, 0755); err != nil {
			t.Fatalf("failed to create new directory: %v", err)
		}

		err := CreateDirectory(newDir, 0755)
		if err != nil {
			t.Fatalf("expected no error, but got %v", err)
		}
	})
}

func TestStringToDurationHookFunc(t *testing.T) {
	t.Run("regular duration string", func(t *testing.T) {
		config := map[string]string{
			"INFERENCE_HEALTH_TIMEOUT": "1m30s",
		}

		var cfg struct {
			InferenceHealthTimeout time.Duration `mapstructure:"INFERENCE_HEALTH_TIMEOUT"`
		}

		v := viper.New()
		for k, val := range config {
			v.Set(k, val)
		}

		err := v.Unmarshal(&cfg, func(dc *mapstructure.DecoderConfig) {
			dc.DecodeHook = StringToDurationHookFunc()
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.InferenceHealthTimeout != 90*time.Second {
			t.Fatalf("expected duration to be 90 seconds, but got %v", cfg.InferenceHealthTimeout)
		}
	})

	t.Run("ISO 8601 duration string", func(t *testing.T) {
		config := map[string]string{
			"INFERENCE_HEALTH_TIMEOUT": "PT1M30S",
		}

		var cfg struct {
			InferenceHealthTimeout time.Duration `mapstructure:"INFERENCE_HEALTH_TIMEOUT"`
		}

		v := viper.New()
		for k, val := range config {
			v.Set(k, val)
		}

		err := v.Unmarshal(&cfg, func(dc *mapstructure.DecoderConfig) {
			dc.DecodeHook = StringToDurationHookFunc()
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.InferenceHealthTimeout != 90*time.Second {
			t.Fatalf("expected duration to be 90 seconds, but got %v", cfg.InferenceHealthTimeout)
		}
	})
}
