// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// otherTenant puts namespace ns in tenant t2; the fixture's other
// namespaces stay in t1 (oneTenant).
func otherTenant(kc *fake.Clientset, ns string) {
	kc.PrependReactor("get", "namespaces", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.GetAction).GetName() != ns {
			return false, nil, nil
		}
		return true, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{testTenantLabel: "t2"}}}, nil
	})
}

// A pod admitted without the webhook can carry any identity. Naming
// another tenant's model gets it nothing: no writer view (which on a
// shared filesystem would be read-write over the other tenant's primary),
// no read-only view, and its Job cannot complete or fail the download.
func TestModelVolumeController_IgnoresIdentityOfAnotherTenant(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	otherTenant(kc, "attacker")
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}

	writer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "attacker",
		Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "writer"},
		Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI}}, Spec: corev1.PodSpec{NodeName: "node-a"}}
	reader := readerPod("attacker", "node-a")
	if _, err := kc.CoreV1().Pods("attacker").Create(ctx, reader, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Handle(ctx, writer)
	c.Handle(ctx, reader)
	if claims, _ := kc.CoreV1().PersistentVolumeClaims("attacker").List(ctx, metav1.ListOptions{}); len(claims.Items) != 0 {
		t.Fatalf("no view of another tenant's primary is minted, got %d claims", len(claims.Items))
	}

	job := downloadJob(1)
	job.Namespace = "attacker"
	c.HandleJob(ctx, job)
	failed := downloadJob(0)
	failed.Namespace = "attacker"
	failed.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
	c.HandleJob(ctx, failed)
	if st, _ := p.Lookup(ctx, mvURI); st.Complete || st.Failed {
		t.Fatalf("another tenant's Job neither completes nor fails the download: %+v", st)
	}
}

// Within a tenant only the namespace holding the download-owner Lease
// writes and finishes the download; every other namespace is a reader.
func TestModelVolumeController_WriterAndJobOnlyFromDownloadOwner(t *testing.T) {
	ctx := context.Background()
	kc, p, minter := sharedFilesystemFixture(t)
	c := &ModelVolumeController{Kube: kc, Provisioner: p, Minter: minter, NodeName: "node-a", Log: logrus.New()}
	writer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "other-ns",
		Labels:      map[string]string{modelvolume.IdentityLabel: modelvolume.Key(mvURI), modelvolume.RoleLabel: "writer"},
		Annotations: map[string]string{modelvolume.IdentityAnnotation: mvURI}}, Spec: corev1.PodSpec{NodeName: "node-a"}}
	c.Handle(ctx, writer)
	if _, err := kc.CoreV1().PersistentVolumeClaims("other-ns").Get(ctx, p.Cfg.WriterViewClaimName(mvURI), metav1.GetOptions{}); err == nil {
		t.Error("a writer outside the owner namespace gets no read-write view")
	}
	job := downloadJob(1)
	job.Namespace = "other-ns"
	c.HandleJob(ctx, job)
	if st, _ := p.Lookup(ctx, mvURI); st.Complete {
		t.Fatal("a Job outside the owner namespace does not complete the download")
	}
	c.HandleJob(ctx, downloadJob(1)) // the owner's
	if st, _ := p.Lookup(ctx, mvURI); !st.Complete {
		t.Fatal("the owner's Job completes it")
	}
	// Completion released the Lease; an agent seeing the event later
	// repeats the outcome without error.
	c.HandleJob(ctx, downloadJob(1))
}

func TestEmptyDirTree(t *testing.T) {
	c := &ModelVolumeController{KubeletPodsDir: "/var/lib/kubelet/pods"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "u1"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{
		{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "c"}}},
		{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}},
	}}}
	root := "/var/lib/kubelet/pods/u1/volumes/kubernetes.io~empty-dir/cache"
	for _, tc := range []struct {
		vol, sub, want string
	}{
		{"cache", "", root},
		{"cache", "cache", root + "/cache"},
		{"cache", "a/b", root + "/a/b"},
		{"cache", "a/../b", root + "/b"},
		{"cache", "..", ""},
		{"cache", "../../../../..", ""},
		{"cache", "a/../../x", ""},
		{"cache", "/etc", ""},
		{"..", "", ""},
		{"../../..", "", ""},
		{"", "", ""},
		{"missing", "", ""},
		{"data", "", ""},
		{"host", "", ""},
	} {
		got, err := c.emptyDirTree(pod, tc.vol, tc.sub)
		if tc.want == "" {
			if err == nil {
				t.Errorf("vol %q sub %q: want refusal, got %q", tc.vol, tc.sub, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("vol %q sub %q: got %q %v, want %q", tc.vol, tc.sub, got, err, tc.want)
		}
	}
}

// A staging pod whose annotation leaves its emptyDir is never copied:
// the agent would otherwise copy host files (kubelet secrets) into a
// volume the tenant can read.
func TestModelVolumeController_StagingOutsideTheEmptyDirIsNotCopied(t *testing.T) {
	ctx := context.Background()
	kc, p, pod := stagingFixture(t, "node-a")
	pod.Annotations[modelvolume.StagingAnnotation] = "../../../"
	c, _, _ := mvController(t, kc, p, "node-a")
	c.StagingAttempts = 1
	measured := false
	c.stageSize = func(string) (int64, error) { measured = true; return 1 << 30, nil }
	c.copyStaging = func(context.Context, string, string, string) error { measured = true; return nil }
	c.Handle(ctx, pod)
	waitUntil(t, "failure recorded", func() bool { st, _ := p.Lookup(ctx, mvURI); return st.Failed })
	if measured {
		t.Error("nothing outside the staging emptyDir is measured or copied")
	}
	if st, _ := p.Lookup(ctx, mvURI); st.Complete {
		t.Error("the identity is not completed")
	}
}

// A rank whose cache annotations leave its emptyDir is neither served nor
// listed as a collection source.
func TestModelVolumeController_RankOutsideTheEmptyDirIsNotServed(t *testing.T) {
	kc, cache, c := cacheController(t, "node-a")
	host := t.TempDir()
	c.HostFSRoot = host
	c.KubeletPodsDir = "/var/lib/kubelet/pods"
	secret := filepath.Join(host, "var/lib/kubelet/pods/victim/volumes/kubernetes.io~secret/token")
	os.MkdirAll(secret, 0o755)
	os.WriteFile(filepath.Join(secret, "token"), []byte("s3cr3t"), 0o644)
	pod := cacheFixture(t, kc, cache, "node-a", 0, 1, true)
	pod.Annotations[modelvolume.CacheRankReadyAnnotation] = "true"
	pod.Annotations[cacheVolumeAnnotation] = ".."
	pod.Annotations[cacheSubpathAnnotation] = "../../victim/volumes/kubernetes.io~secret"
	if _, err := kc.CoreV1().Pods("sr-fn").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	r := mux.NewRouter()
	r.HandleFunc("/v1/cache-rank/{key}/{ordinal}", c.ServeRank)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/cache-rank/"+modelvolume.Key("cache://abc123")+"/0", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("a rank outside its emptyDir is refused, got %d", w.Code)
	}
	if ranks, _, _, ok, err := c.groupRanks(context.Background(), "cache://abc123"); err != nil || ok || len(ranks) != 0 {
		t.Errorf("the rank is not a collection source: %v %v %v", ranks, ok, err)
	}
}

// A Block-mode staging Job that fails without its pod showing it
// (deadline exceeded, evicted past its backoff limit) is given up: the
// failure is recorded and readers pending on the claim are released.
func TestModelVolumeController_FailedStagingJobReleasesReaders(t *testing.T) {
	ctx := context.Background()
	kc, p, _ := stagingFixture(t, "node-a")
	pending := readerPod("other-ns", "")
	pending.Name = "mini-service-0"
	if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, pending, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c, _, _ := mvController(t, kc, p, "node-b")
	job := downloadJob(0)
	job.Annotations[modelvolume.StagingAnnotation] = "ngc-models"
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"}}
	c.HandleJob(ctx, job)
	waitUntil(t, "failure recorded", func() bool { st, _ := p.Lookup(ctx, mvURI); return st.Failed })
	waitUntil(t, "pending reader released", func() bool {
		_, err := kc.CoreV1().Pods("other-ns").Get(ctx, "mini-service-0", metav1.GetOptions{})
		return err != nil
	})
}

// The backstop: a reader pending on an identity nothing produces any
// more (the download Job deleted by hand, its owner gone) is released
// once it is older than the owner grace; a reader whose download is
// still owned and running stays.
func TestModelVolumeController_StrandedPendingReaderReleased(t *testing.T) {
	ctx := context.Background()
	kc, p, _ := stagingFixture(t, "node-a")
	p.Cfg.Reader = modelvolume.ReaderPVC
	old := readerPod("other-ns", "")
	old.Name = "stranded-0"
	old.CreationTimestamp = metav1.NewTime(time.Now().Add(-modelvolume.DownloadOwnerGrace - time.Minute))
	young := readerPod("other-ns", "")
	young.Name = "young-0"
	young.CreationTimestamp = metav1.NewTime(time.Now())
	for _, r := range []*corev1.Pod{old, young} {
		if _, err := kc.CoreV1().Pods("other-ns").Create(ctx, r, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := mvController(t, kc, p, "node-b")
	c.Handle(ctx, old)
	if _, err := kc.CoreV1().Pods("other-ns").Get(ctx, "stranded-0", metav1.GetOptions{}); err != nil {
		t.Fatal("a reader whose download is owned and recent stays")
	}
	// The Job is deleted by hand and the owner's claim is past its grace.
	if err := kc.BatchV1().Jobs("sr-fn").Delete(ctx, modelvolume.JobName(mvURI), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	p.Now = func() time.Time { return time.Now().Add(modelvolume.DownloadOwnerGrace + time.Minute) }
	c.mu.Lock()
	c.checked = nil
	c.mu.Unlock()
	c.Handle(ctx, young)
	if _, err := kc.CoreV1().Pods("other-ns").Get(ctx, "young-0", metav1.GetOptions{}); err != nil {
		t.Fatal("a reader younger than the owner grace stays")
	}
	c.mu.Lock()
	c.checked = nil
	c.mu.Unlock()
	c.Handle(ctx, old)
	if _, err := kc.CoreV1().Pods("other-ns").Get(ctx, "stranded-0", metav1.GetOptions{}); err == nil {
		t.Error("a reader pending on an identity nothing produces is released")
	}
}
