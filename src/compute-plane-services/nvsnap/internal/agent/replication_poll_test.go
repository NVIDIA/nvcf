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

package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
)

const testHashA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const testHashB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// TestExtractCaptureHashes asserts only "<64-hex>/manifest.json" keys are
// matched — tree files, partial keys, short/garbage hashes are ignored,
// and duplicates collapse.
func TestExtractCaptureHashes(t *testing.T) {
	objs := []objectstore.ObjectInfo{
		{Key: testHashA + "/manifest.json"},
		{Key: testHashA + "/tree/rootfs/weights.bin"}, // ignored: tree file
		{Key: testHashB + "/manifest.json"},
		{Key: testHashB + "/tree/x"},          // ignored
		{Key: "notahash/manifest.json"},       // ignored: not 64-hex
		{Key: "deadbeef/manifest.json"},       // ignored: too short
		{Key: testHashA + "/manifest.jsonbk"}, // ignored: suffix
		{Key: "manifest.json"},                // ignored: no hash prefix
		{Key: testHashA + "/manifest.json"},   // duplicate of A
	}
	got := extractCaptureHashes(objs)
	sort.Strings(got)
	want := []string{testHashA, testHashB}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("extractCaptureHashes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractCaptureHashes = %v, want %v", got, want)
		}
	}
}

// agentWithLocalGPU builds an agent whose local node advertises the given
// driver major + compute capability via node labels (the same source the
// real localGPUComputeCapability reads). driverMajor 0 / cc "" leave the
// respective local value unknown.
func agentWithLocalGPU(driverMajor int, cc string) *Agent {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{}},
	}
	if cc != "" {
		// "9.0" → major "9", minor "0".
		parts := strings.SplitN(cc, ".", 2)
		node.Labels["nvidia.com/gpu.compute.major"] = parts[0]
		if len(parts) > 1 {
			node.Labels["nvidia.com/gpu.compute.minor"] = parts[1]
		}
	}
	a := &Agent{log: logrus.New(), kubeClient: fake.NewSimpleClientset(node)}
	a.config.NodeName = "node-a"
	a.config.RootfsCapture.CUDADriverMajor = driverMajor
	return a
}

func TestCaptureIsCompatible(t *testing.T) {
	log := logrus.New().WithField("test", "compat")
	cases := []struct {
		name        string
		localDriver int
		localCC     string
		meta        map[string]string
		want        bool
	}{
		{
			name:        "matching driver and gpu → compatible",
			localDriver: 580, localCC: "9.0",
			meta: map[string]string{"driver_major": "580", "gpu_compute_capability": "9.0"},
			want: true,
		},
		{
			name:        "mismatched driver_major → skip",
			localDriver: 580, localCC: "9.0",
			meta: map[string]string{"driver_major": "570", "gpu_compute_capability": "9.0"},
			want: false,
		},
		{
			name:        "mismatched gpu cc → skip",
			localDriver: 580, localCC: "9.0",
			meta: map[string]string{"driver_major": "580", "gpu_compute_capability": "8.0"},
			want: false,
		},
		{
			name:        "local driver unknown → fail open on driver",
			localDriver: 0, localCC: "9.0",
			meta: map[string]string{"driver_major": "570", "gpu_compute_capability": "9.0"},
			want: true,
		},
		{
			name:        "local cc unknown → fail open on gpu",
			localDriver: 580, localCC: "",
			meta: map[string]string{"driver_major": "580", "gpu_compute_capability": "8.0"},
			want: true,
		},
		{
			name:        "source values empty → fail open both",
			localDriver: 580, localCC: "9.0",
			meta: map[string]string{},
			want: true,
		},
		{
			name:        "malformed source driver_major → fail open on driver",
			localDriver: 580, localCC: "9.0",
			meta: map[string]string{"driver_major": "five-eighty", "gpu_compute_capability": "9.0"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := agentWithLocalGPU(tc.localDriver, tc.localCC)
			if got := a.captureIsCompatible(tc.meta, log); got != tc.want {
				t.Errorf("captureIsCompatible(%v) = %v, want %v", tc.meta, got, tc.want)
			}
		})
	}
}

// TestLocalGPUComputeCapability_FailOpenWhenUnreadable: no kubeClient or
// no NodeName → "" (so the compat gate fails open on the GPU dimension).
func TestLocalGPUComputeCapability_FailOpenWhenUnreadable(t *testing.T) {
	a := &Agent{log: logrus.New()}
	if got := a.localGPUComputeCapability(); got != "" {
		t.Errorf("no kubeClient: localGPUComputeCapability = %q, want empty", got)
	}
	a.kubeClient = fake.NewSimpleClientset()
	a.config.NodeName = "" // unknown node name
	if got := a.localGPUComputeCapability(); got != "" {
		t.Errorf("empty NodeName: localGPUComputeCapability = %q, want empty", got)
	}
}

// TestStartReplicationPoller_DisabledNoop: the poller is a silent no-op (no panic,
// no goroutine work) when replication is off or the interval is 0.
func TestStartReplicationPoller_DisabledNoop(t *testing.T) {
	t.Run("replication disabled", func(t *testing.T) {
		a := &Agent{log: logrus.New()}
		a.config.Replication.PollInterval = 60 // > 0 but no objectStoreHome
		a.startReplicationPoller(t.Context())
	})
	t.Run("interval zero", func(t *testing.T) {
		a := &Agent{log: logrus.New(), objectStoreHome: newFakeBucket("home")}
		a.config.Replication.PollInterval = 0
		a.startReplicationPoller(t.Context())
	})
}

func TestReplicationPollerNamespace(t *testing.T) {
	a := &Agent{}
	if got := a.replicationPollerNamespace(); got != "nvsnap-system" {
		t.Errorf("default namespace = %q, want nvsnap-system", got)
	}
	a.config.L2.Namespace = "nvcf-system"
	if got := a.replicationPollerNamespace(); got != "nvcf-system" {
		t.Errorf("configured namespace = %q, want nvcf-system", got)
	}
}
