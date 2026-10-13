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

package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"nvcf-cli/internal/selfhosted/controlplaneprofile"
)

func stubEventLedgerDeployed(t *testing.T, deployed bool, err error) {
	t.Helper()
	prev := eventLedgerDeployed
	t.Cleanup(func() { eventLedgerDeployed = prev })
	eventLedgerDeployed = func(context.Context, string) (bool, error) { return deployed, err }
}

func TestResolveEventLedgerAddon(t *testing.T) {
	const icmsURL = "https://sis.example.com"

	t.Run("not deployed leaves the collector off", func(t *testing.T) {
		stubEventLedgerDeployed(t, false, nil)
		got := resolveEventLedgerAddon(context.Background(), "prd", "cp", "gpu", icmsURL)
		assert.False(t, got.Enabled)
		assert.Empty(t, got.ServiceURL)
	})

	t.Run("lookup failure leaves the collector off", func(t *testing.T) {
		stubEventLedgerDeployed(t, false, errors.New("cluster unreachable"))
		got := resolveEventLedgerAddon(context.Background(), "prd", "cp", "gpu", icmsURL)
		assert.False(t, got.Enabled)
		assert.Empty(t, got.ServiceURL)
	})

	t.Run("single cluster enables without a URL", func(t *testing.T) {
		stubEventLedgerDeployed(t, true, nil)
		got := resolveEventLedgerAddon(context.Background(), "prd", "", "", icmsURL)
		assert.True(t, got.Enabled)
		assert.Empty(t, got.ServiceURL)
	})

	t.Run("split cluster derives the events URL from the ICMS URL", func(t *testing.T) {
		stubEventLedgerDeployed(t, true, nil)
		got := resolveEventLedgerAddon(context.Background(), "prd", "cp", "gpu", icmsURL)
		assert.True(t, got.Enabled)
		assert.Equal(t, "https://events.example.com", got.ServiceURL)
	})

	t.Run("nil context is tolerated", func(t *testing.T) {
		stubEventLedgerDeployed(t, true, nil)
		got := resolveEventLedgerAddon(nil, "prd", "", "", icmsURL) //nolint:staticcheck
		assert.True(t, got.Enabled)
	})
}

func TestNewSelfManagedValuesEventLedger(t *testing.T) {
	enabled := registerEndpointValues{EventLedger: controlplaneprofile.EventLedgerAddon{Enabled: true, ServiceURL: "https://events.example.com"}}
	got := newSelfManagedValuesFromEndpoints("psat", "us-west-1", enabled)
	assert.Equal(t, "https://events.example.com", got.EventLedgerServiceURL)
	if assert.NotNil(t, got.OTelCollector) {
		assert.True(t, got.OTelCollector.Enabled)
	}

	disabled := newSelfManagedValuesFromEndpoints("psat", "us-west-1", registerEndpointValues{})
	assert.Nil(t, disabled.OTelCollector)
	assert.Empty(t, disabled.EventLedgerServiceURL)
}
