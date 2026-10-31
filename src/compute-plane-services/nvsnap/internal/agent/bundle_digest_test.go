// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// writeBundle writes a small bundle version tree: an executable, a library
// symlink and a nested file.
func writeBundle(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "criu"), []byte("criu binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "libc.so.6"), []byte("libc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libc.so.6", filepath.Join(dir, "lib", "libc.so")); err != nil {
		t.Fatal(err)
	}
}

// The digest changes with any content, permission or link target, and
// with nothing else.
func TestBundleDigest(t *testing.T) {
	base := t.TempDir()
	writeBundle(t, base)
	want, err := bundleDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	same := t.TempDir()
	writeBundle(t, same)
	if got, _ := bundleDigest(same); got != want {
		t.Errorf("same tree, other digest: %s vs %s", got, want)
	}
	for name, change := range map[string]func(dir string) error{
		"contents":    func(d string) error { return os.WriteFile(filepath.Join(d, "criu"), []byte("other binary"), 0o755) },
		"permissions": func(d string) error { return os.Chmod(filepath.Join(d, "criu"), 0o644) },
		"link target": func(d string) error {
			if err := os.Remove(filepath.Join(d, "lib", "libc.so")); err != nil {
				return err
			}
			return os.Symlink("/etc/passwd", filepath.Join(d, "lib", "libc.so"))
		},
		"an added file": func(d string) error { return os.WriteFile(filepath.Join(d, "extra"), nil, 0o644) },
	} {
		dir := t.TempDir()
		writeBundle(t, dir)
		if err := change(dir); err != nil {
			t.Fatal(err)
		}
		if got, _ := bundleDigest(dir); got == want {
			t.Errorf("%s changed, the digest did not", name)
		}
	}
}

func digestAgent(t *testing.T, kc *fake.Clientset, node, arch string) *Agent {
	t.Helper()
	if _, err := kc.CoreV1().Nodes().Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: node, Labels: map[string]string{corev1.LabelArchStable: arch}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{kubeClient: kc, log: logrus.New(), config: Config{NodeName: node}}
	a.config.Webhook.HostBundleRoot = t.TempDir()
	return a
}

// stage puts bundle version id on the agent's node as the staging init does.
func stage(t *testing.T, a *Agent, id string, change func(dir string)) {
	t.Helper()
	dir := filepath.Join(a.bundleRoot(), webhook.BundleVersionsDir, id)
	writeBundle(t, dir)
	if change != nil {
		change(dir)
	}
	if err := os.WriteFile(filepath.Join(a.bundleRoot(), "current-bundle"), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A fetched version is installed only when its contents are the ones its
// staging agents recorded for that architecture (review: a version fetched
// from a peer was trusted by its id alone).
func TestFetchedBundleIsVerified(t *testing.T) {
	ctx := context.Background()
	recordedBundles.Clear()
	kc := fake.NewSimpleClientset()
	arm := digestAgent(t, kc, "arm-a", "arm64")
	stage(t, arm, "v1.2.3", nil)
	arm.recordBundleDigest(ctx)
	x86 := digestAgent(t, kc, "x86-a", "amd64")
	stage(t, x86, "v1.2.3", func(d string) { _ = os.WriteFile(filepath.Join(d, "criu"), []byte("x86 criu"), 0o755) })
	x86.recordBundleDigest(ctx)

	fetched := func(change func(string)) string {
		dir := t.TempDir()
		writeBundle(t, dir)
		if change != nil {
			change(dir)
		}
		return dir
	}
	verifier := digestAgent(t, kc, "arm-b", "arm64")
	if err := verifier.verifyBundle(ctx, "v1.2.3", "arm64", fetched(nil)); err != nil {
		t.Errorf("the recorded contents were refused: %v", err)
	}
	tampered := fetched(func(d string) { _ = os.WriteFile(filepath.Join(d, "criu"), []byte("planted"), 0o755) })
	if err := verifier.verifyBundle(ctx, "v1.2.3", "arm64", tampered); err == nil {
		t.Error("contents other than the recorded ones were accepted")
	}
	if err := verifier.verifyBundle(ctx, "v9.9.9", "arm64", fetched(nil)); err == nil {
		t.Error("a version without a recorded digest was accepted")
	}
	// Each architecture has its own binaries under the same version id.
	if err := verifier.verifyBundle(ctx, "v1.2.3", "amd64", fetched(nil)); err == nil {
		t.Error("arm64 contents were accepted as the amd64 version")
	}
}

// The first record of a version stands: a node that staged other contents
// under the same id does not replace it.
func TestBundleDigestFirstRecordStands(t *testing.T) {
	ctx := context.Background()
	recordedBundles.Clear()
	kc := fake.NewSimpleClientset()
	first := digestAgent(t, kc, "n1", "arm64")
	stage(t, first, "v2", nil)
	first.recordBundleDigest(ctx)
	cm, err := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, bundleDigestsConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := cm.Data[bundleDigestKey("v2", "arm64")]
	if want == "" {
		t.Fatal("no digest recorded")
	}
	recordedBundles.Clear() // a second agent process
	other := digestAgent(t, kc, "n2", "arm64")
	stage(t, other, "v2", func(d string) { _ = os.WriteFile(filepath.Join(d, "criu"), []byte("drifted"), 0o755) })
	other.recordBundleDigest(ctx)
	cm, _ = kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, bundleDigestsConfigMap, metav1.GetOptions{})
	if got := cm.Data[bundleDigestKey("v2", "arm64")]; got != want {
		t.Errorf("the record was replaced: %s, want %s", got, want)
	}
}
