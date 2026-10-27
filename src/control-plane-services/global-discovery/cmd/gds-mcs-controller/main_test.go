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
	"context"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/config"
)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		code int
	}{
		{"help", []string{"-h"}, 0},
		{"unknown flag", []string{"--nope"}, 2},
		{"invalid", []string{"--leader-elect=false"}, 2},
		{"valid", []string{"--gds-url=https://gds.example", "--leader-elect=false", "--zap-log-level=debug"}, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg, zapOpts, code := parse(flag.NewFlagSet("test", flag.ContinueOnError), tt.args, &out)
			assert.Equal(t, tt.code, code, out.String())
			assert.NotNil(t, zapOpts)
			if code == -1 {
				assert.Equal(t, []string{"https://gds.example"}, cfg.GDSURLs)
			}
		})
	}
}

func TestRunWithoutAKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "/does/not/exist")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("HOME", t.TempDir())
	cfg := config.Config{GDSURLs: []string{"https://gds.example"}}
	assert.Equal(t, 1, run(context.Background(), cfg))
	cfg.OTelEnabled = true
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	assert.Equal(t, 1, run(context.Background(), cfg))
}
