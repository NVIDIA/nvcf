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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/cmd"
)

func TestRun(t *testing.T) {
	rootTestdataDir := filepath.Join("..", "..", "testdata", "icms-translate")
	for _, nvType := range []string{"function", "task"} {
		for _, fType := range []string{"container", "helmchart"} {
			testdataDir := filepath.Join(rootTestdataDir, nvType, fType)
			dirs, err := os.ReadDir(testdataDir)
			require.NoError(t, err)
			for _, dir := range dirs {
				caseName := strings.Join([]string{nvType, fType, dir.Name()}, "_")
				t.Run(caseName, func(t *testing.T) {
					testdataDir := filepath.Join(testdataDir, dir.Name())
					runCase(t, testdataDir)
				})
			}
		}
	}
}

func TestRun_sidecar_deployments(t *testing.T) {
	rootTestdataDir := filepath.Join("..", "..", "testdata", "icms-translate")
	for _, nvType := range []string{"function"} {
		for _, fType := range []string{"container", "helmchart"} {
			testdataDir := filepath.Join(rootTestdataDir, nvType, fType)
			dirs, err := os.ReadDir(testdataDir)
			require.NoError(t, err)
			for _, dir := range dirs {
				caseName := strings.Join([]string{nvType, fType, dir.Name()}, "_")
				testdataDir := filepath.Join(testdataDir, dir.Name())
				if _, err := os.Stat(filepath.Join(testdataDir, "config_utdep.json")); err == nil || os.IsExist(err) {
					t.Run(caseName, func(t *testing.T) {
						runCaseUtDep(t, testdataDir)
					})
				}
			}
		}
	}
}

func runCase(t *testing.T, dir string) {
	t.Helper()
	out := &bytes.Buffer{}

	cmd := cmd.NewTranslateCommand()
	app := &cli.App{
		Name:      cmd.Name,
		Usage:     cmd.Usage,
		Flags:     cmd.Flags,
		Action:    cmd.Action,
		Writer:    out,
		ErrWriter: out,
	}

	expBytes, err := os.ReadFile(filepath.Join(dir, "exp.yaml"))
	require.NoError(t, err)

	err = app.Run([]string{"main",
		"-m", filepath.Join(dir, "message.json"),
		"-c", filepath.Join(dir, "config.json"),
	})
	require.NoError(t, err)

	assert.Equal(t, string(expBytes), out.String())
}

func runCaseUtDep(t *testing.T, dir string) {
	t.Helper()
	out := &bytes.Buffer{}

	cmd := cmd.NewTranslateCommand()
	app := &cli.App{
		Name:      cmd.Name,
		Usage:     cmd.Usage,
		Flags:     cmd.Flags,
		Action:    cmd.Action,
		Writer:    out,
		ErrWriter: out,
	}

	expBytes, err := os.ReadFile(filepath.Join(dir, "exp_utdep.yaml"))
	require.NoError(t, err)

	err = app.Run([]string{"main",
		"-m", filepath.Join(dir, "message.json"),
		"-c", filepath.Join(dir, "config_utdep.json"),
	})
	require.NoError(t, err)

	assert.Equal(t, string(expBytes), out.String())
}
