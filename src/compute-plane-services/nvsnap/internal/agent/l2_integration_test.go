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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func TestVRAMGBFromGPUType(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"NVIDIA-H100-80GB-HBM3", "80"},
		{"NVIDIA-A100-40GB", "40"},
		{"NVIDIA-A100-80GB-PCIe", "80"},
		{"NVIDIA-L4", ""},    // no GB token
		{"", ""},             // empty input
		{"some-junk", ""},    // no token ending in GB
		{"NVIDIA-X-0GB", ""}, // zero is not a positive vRAM
		{"NVIDIA-X-abcGB", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := vramGBFromGPUType(tc.in)
			if got != tc.want {
				t.Errorf("vramGBFromGPUType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDefaultL2Size_BaselineH100(t *testing.T) {
	// No manifest info → 80 GiB × 1 × 1.2 = 96 GiB.
	got := defaultL2Size("any-hash", checkpointstore.Manifest{})
	want := int64(96) * (1 << 30)
	if got != want {
		t.Errorf("default H100 1× = %d GiB, want %d GiB",
			got/(1<<30), want/(1<<30))
	}
}

func TestDefaultL2Size_HonorsManifestHints(t *testing.T) {
	m := checkpointstore.Manifest{
		SourcePodMeta: map[string]string{
			"gpu_vram_gb": "40", // A100-40GB
			"gpu_count":   "4",
		},
	}
	got := defaultL2Size("hash", m)
	want := int64(40*4) * (1 << 30) * 12 / 10
	if got != want {
		t.Errorf("4× A100 40GB = %d GiB, want %d GiB",
			got/(1<<30), want/(1<<30))
	}
}

// Rootfs captures size from the measured on-disk tree, NOT vRAM. This is
// the gpt-oss/DeepSeek bug: every rootfs capture used to fall through to
// the 96 GiB vRAM default because the rootfs path never sets the GPU
// hints, so a >96 GB model silently overflowed the writer Job.
func TestDefaultL2Size_RootfsUsesTotalSize(t *testing.T) {
	const oneGiB = int64(1) << 30
	// A 140 GB fp16-70B-class on-disk tree → 140 × 1.2 = 168 GiB. The
	// old vRAM default would have been a too-small 96 GiB.
	m := checkpointstore.Manifest{
		CaptureMethod:  "rootfs",
		TotalSizeBytes: 140 * oneGiB,
		// GPU hints deliberately absent — the rootfs path never sets them.
	}
	got := defaultL2Size("hash", m)
	want := 140 * oneGiB * 12 / 10
	if got != want {
		t.Errorf("rootfs 140 GB tree → %d GiB, want %d GiB (must size from TotalSizeBytes, not vRAM)",
			got/oneGiB, want/oneGiB)
	}
	if got <= 96*oneGiB {
		t.Errorf("rootfs 140 GB tree sized at %d GiB — still ≤ the 96 GiB vRAM default (bug not fixed)", got/oneGiB)
	}
}

// Small rootfs captures get a 10 GiB floor so fs overhead on a tiny tree
// doesn't provision a sub-GiB PVC that fails to format/fit.
func TestDefaultL2Size_RootfsFloor(t *testing.T) {
	const oneGiB = int64(1) << 30
	m := checkpointstore.Manifest{CaptureMethod: "rootfs", TotalSizeBytes: 2 * oneGiB}
	if got := defaultL2Size("hash", m); got != 10*oneGiB {
		t.Errorf("2 GB rootfs tree → %d GiB, want 10 GiB floor", got/oneGiB)
	}
}

// A rootfs capture with no measured size (TotalSizeBytes==0, shouldn't
// happen but be safe) falls back to the vRAM formula rather than
// provisioning a zero/floor-only PVC blindly.
func TestDefaultL2Size_RootfsNoSizeFallsBackToVRAM(t *testing.T) {
	m := checkpointstore.Manifest{CaptureMethod: "rootfs", TotalSizeBytes: 0}
	want := int64(80) * (1 << 30) * 12 / 10 // 96 GiB default
	if got := defaultL2Size("hash", m); got != want {
		t.Errorf("rootfs w/ no size → %d GiB, want vRAM default %d GiB", got/(1<<30), want/(1<<30))
	}
}

// pvcStateHTTPWriter: hits a httptest.Server and confirms the body
// matches the documented JSON contract + headers are set. URL is
// hash-keyed (nvsnap#76): the producer only ever has the content
// hash, never a per-capture id.
func TestPVCStateHTTPWriter_PostsExpectedBody(t *testing.T) {
	var gotURL, gotBody, gotContentType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	const hash = "a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b"
	w := NewPVCStateHTTPWriter(srv.URL)
	if err := w.UpdatePVCPromoteState(hash, "writing", ""); err != nil {
		t.Fatalf("UpdatePVCPromoteState: %v", err)
	}
	wantURL := "/api/v1/checkpoints/by-hash/" + hash + "/pvc-state"
	if gotURL != wantURL {
		t.Errorf("URL = %q, want %q", gotURL, wantURL)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotBody == "" || !contains(gotBody, `"state":"writing"`) {
		t.Errorf("body = %q, want JSON with state=writing", gotBody)
	}
}

func TestPVCStateHTTPWriter_PropagatesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("db down"))
	}))
	defer srv.Close()
	w := NewPVCStateHTTPWriter(srv.URL)
	err := w.UpdatePVCPromoteState("a4f7818605da321ee9c3cb80bb5e6fe7289bac9736d153e04e67e2e3f4a7407b", "ready", "rox-abc")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !contains(err.Error(), "500") || !contains(err.Error(), "db down") {
		t.Errorf("error doesn't include status + body: %v", err)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// L2 storage class validation at agent startup. Post-nvsnap#81, the
// validator no longer denies any provisioner — the L2 backend creates
// the writer PVC as ReadWriteOnce + the reader PVC as ReadOnlyMany,
// so every standard CSI works (Hyperdisk ML included). Tests:
//   * happy path on a few representative provisioners
//   * clear error when the SC doesn't exist (the only remaining
//     hard-fail case at startup)

func TestValidateL2StorageClass_AcceptsHyperdiskML(t *testing.T) {
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "hyperdisk-ml"},
		Provisioner: "pd.csi.storage.gke.io",
		Parameters:  map[string]string{"type": "hyperdisk-ml"},
	}
	kc := kubefake.NewSimpleClientset(sc)
	if err := validateL2StorageClass(context.Background(), kc, "hyperdisk-ml",
		logrus.New().WithField("test", "l2-sc")); err != nil {
		t.Errorf("hyperdisk-ml must be accepted post-nvsnap#81 (RWO writer + ROX reader, no RWX needed); got %v", err)
	}
}

func TestValidateL2StorageClass_AcceptsCommonProvisioners(t *testing.T) {
	for _, p := range []string{
		"pd.csi.storage.gke.io",
		"filestore.csi.storage.gke.io",
		"efs.csi.aws.com",
		"file.csi.azure.com",
		"driver.longhorn.io",
		"rook-cephfs.csi.ceph.com",
	} {
		t.Run(p, func(t *testing.T) {
			sc := &storagev1.StorageClass{
				ObjectMeta:  metav1.ObjectMeta{Name: "test"},
				Provisioner: p,
			}
			kc := kubefake.NewSimpleClientset(sc)
			if err := validateL2StorageClass(context.Background(), kc, "test",
				logrus.New().WithField("test", "l2-sc")); err != nil {
				t.Errorf("provisioner %s must pass startup probe; got %v", p, err)
			}
		})
	}
}

func TestValidateL2StorageClass_StorageClassNotFound(t *testing.T) {
	// agent.l2.storageClass set to a name the cluster doesn't have.
	// Refuse to come up with a clear error — silent provisioning hang
	// would otherwise blame the wrong layer at debug time.
	kc := kubefake.NewSimpleClientset()
	err := validateL2StorageClass(context.Background(), kc, "missing-sc",
		logrus.New().WithField("test", "l2-sc"))
	if err == nil {
		t.Fatal("missing StorageClass must fail validation; got nil")
	}
	if !strings.Contains(err.Error(), "missing-sc") {
		t.Errorf("error should name the missing class; got: %v", err)
	}
}

// A cachedir capture must size from what it measured. cachedir mode was added
// after defaultL2Size and did not match its rootfs-only check, so every
// cachedir capture fell through to the vRAM estimate: a 14 GB capture asked
// for 96 GiB (80 GiB H100 default x 1.2) and stranded the difference, since
// the L2 StorageClass is typically Retain.
func TestDefaultL2Size_CacheDirUsesTotalSize(t *testing.T) {
	const oneGiB int64 = 1 << 30
	m := checkpointstore.Manifest{CaptureMethod: "cachedir", TotalSizeBytes: 14 * oneGiB}

	got := defaultL2Size("hash", m)
	if want := 14 * oneGiB * 12 / 10; got != want {
		t.Errorf("defaultL2Size = %d GiB, want %d GiB (measured x1.2)",
			got/oneGiB, want/oneGiB)
	}
	if got >= 80*oneGiB {
		t.Errorf("cachedir capture fell through to the vRAM estimate (%d GiB)",
			got/oneGiB)
	}
}
