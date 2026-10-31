// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A bundle version holds binaries that run in other pods and in the restore
// path (criu, tar, nvsnap-helper). A node that lacks a version fetches it
// from a peer agent over plain HTTP, and the version id (usually the image
// version) proves nothing about the bytes. So each agent records the digest
// of the version it staged in a ConfigMap only agents write, and a fetched
// version is installed only when its digest matches the recorded one.

// bundleDigestsConfigMap maps a bundle version and architecture
// (bundleDigestKey) to the version's digest.
const bundleDigestsConfigMap = "nvsnap-bundle-digests"

// bundleDigestKey names a version's digest: a version id names one image
// version, which holds each architecture's own binaries.
func bundleDigestKey(id, arch string) string { return id + "." + arch }

// bundleDigest is the digest of the bundle version tree at dir: every
// entry's path, type, permissions, symlink target and file contents, in
// path order.
func bundleDigest(dir string) (string, error) {
	type entry struct{ rel, line string }
	var ents []entry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s %o", info.Mode().Type().String(), info.Mode().Perm())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			line += " -> " + target
		case info.Mode().IsRegular():
			f, err := os.Open(p) //nolint:gosec // p is under dir, walked by WalkDir
			if err != nil {
				return err
			}
			h := sha256.New()
			_, cerr := io.Copy(h, f)
			_ = f.Close()
			if cerr != nil {
				return cerr
			}
			line += " " + hex.EncodeToString(h.Sum(nil))
		}
		ents = append(ents, entry{rel: filepath.ToSlash(rel), line: line})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].rel < ents[j].rel })
	h := sha256.New()
	for _, e := range ents {
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", e.rel, e.line)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var recordedBundles sync.Map // bundle id -> struct{}: recorded by this agent

// recordBundleDigest records the digest of the bundle version this agent
// staged, once per version. A version already recorded with another digest
// is left as recorded: the first record is the one fetches trust, and a
// node whose staging differs is reported.
func (a *Agent) recordBundleDigest(ctx context.Context) {
	id := a.currentBundle()
	if id == "" || a.kubeClient == nil {
		return
	}
	if _, done := recordedBundles.Load(id); done {
		return
	}
	arch := a.nodeArch(ctx, a.config.NodeName)
	if arch == "" {
		a.log.WithField("bundle", id).Warn("cannot read this node's architecture; the staged bundle version's digest is not recorded")
		return
	}
	key := bundleDigestKey(id, arch)
	digest, err := bundleDigest(filepath.Join(a.bundleVersionsDir(), id))
	if err != nil {
		a.log.WithError(err).WithField("bundle", id).Warn("cannot digest the staged bundle version; peers cannot fetch it")
		return
	}
	ns := a.criuFailedNamespace()
	cms := a.kubeClient.CoreV1().ConfigMaps(ns)
	for attempt := 0; attempt < 3; attempt++ {
		cm, err := cms.Get(ctx, bundleDigestsConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = cms.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: bundleDigestsConfigMap, Namespace: ns},
				Data:       map[string]string{key: digest},
			}, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
		} else if err == nil {
			switch recorded := cm.Data[key]; {
			case recorded == digest:
			case recorded != "":
				a.log.WithFields(map[string]interface{}{"bundle": id, "recorded": recorded, "staged": digest}).
					Warn("this node staged bundle version with other contents than the recorded ones; peers fetching it get the recorded ones")
			default:
				if cm.Data == nil {
					cm.Data = map[string]string{}
				}
				cm.Data[key] = digest
				_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
				if apierrors.IsConflict(err) {
					continue
				}
			}
		}
		if err != nil {
			a.log.WithError(err).WithField("bundle", id).Warn("cannot record the staged bundle version's digest")
			return
		}
		recordedBundles.Store(id, struct{}{})
		return
	}
}

// verifyBundle checks the bundle version id of architecture arch,
// extracted at dir, against the digest recorded for it; a version without
// one is refused.
func (a *Agent) verifyBundle(ctx context.Context, id, arch, dir string) error {
	cm, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, bundleDigestsConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("bundle version %s: no recorded digest to check it against: %w", id, err)
	}
	want := cm.Data[bundleDigestKey(id, arch)]
	if want == "" {
		return fmt.Errorf("bundle version %s: no recorded digest to check it against", id)
	}
	got, err := bundleDigest(dir)
	if err != nil {
		return fmt.Errorf("bundle version %s: %w", id, err)
	}
	if got != want {
		return fmt.Errorf("bundle version %s: fetched contents (digest %s) are not the recorded ones (%s)", id, got, want)
	}
	return nil
}
