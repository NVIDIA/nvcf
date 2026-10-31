// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"testing"

	"k8s.io/client-go/rest"
)

// Every client the agent builds carries the configured limits, not
// client-go's default of 5 requests a second that starved admission.
func TestWithClientLimits(t *testing.T) {
	defer func(q float32, b int) { KubeClientQPS, KubeClientBurst = q, b }(KubeClientQPS, KubeClientBurst)
	if KubeClientQPS < 20 || KubeClientBurst < 40 {
		t.Errorf("defaults too low for a webhook sharing its client: qps %v burst %d", KubeClientQPS, KubeClientBurst)
	}
	KubeClientQPS, KubeClientBurst = 7, 9
	cfg := withClientLimits(&rest.Config{})
	if cfg.QPS != 7 || cfg.Burst != 9 {
		t.Errorf("limits not applied: %v %d", cfg.QPS, cfg.Burst)
	}
}
