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

package polling

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

type natsError string

func (e natsError) Error() string { return string(e) }

const errNatsNotReady natsError = "embedded nats server not ready for connections"

// startEmbeddedNats starts an in-process NATS server on an OS-assigned
// ephemeral port (Port: -1) and returns its client URL. Using an ephemeral
// port (instead of testutils.NewNatsSuperCluster, which hardcodes 4222/4282)
// lets this package run in parallel with the worker package under
// `go test ./...` without a port collision and the resulting healthz-backoff
// hang. The server is torn down via t.Cleanup.
func startEmbeddedNats(t *testing.T) (string, error) {
	t.Helper()

	opts := &server.Options{
		Host:                   "127.0.0.1",
		Port:                   -1,
		JetStream:              true,
		StoreDir:               t.TempDir(),
		DisableJetStreamBanner: true,
		Tags:                   []string{"aws-region:region-1", "aws-region:region-2", "aws-region:region-3"},
		NoSigs:                 true,
	}

	s, err := server.NewServer(opts)
	if err != nil {
		return "", err
	}
	s.Start()
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})

	if !s.ReadyForConnections(10 * time.Second) {
		return "", errNatsNotReady
	}
	return s.ClientURL(), nil
}
