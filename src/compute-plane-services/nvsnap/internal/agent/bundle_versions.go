// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Node bundle versions (docs/design/VERSIONED-NODE-BUNDLE.md). The staging
// init container stages each bundle version into its own immutable
// <root>/versions/<id> and records the id in <root>/current-bundle. Pods
// mount a version; captures record it; a restore placeholder mounts the
// version its checkpoint recorded, fetched from a peer agent when this node
// lacks it; the reaper removes versions nothing uses.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// bundleRoot is the node bundle root as the agent sees it (the chart
// mounts it at the same path as on the host).
func (a *Agent) bundleRoot() string {
	if r := a.config.Webhook.HostBundleRoot; r != "" {
		return r
	}
	return webhook.DefaultHostBundleRoot
}

// bundleVersionsDir holds the node's bundle versions.
func (a *Agent) bundleVersionsDir() string {
	return filepath.Join(a.bundleRoot(), webhook.BundleVersionsDir)
}

// currentBundle is the bundle version this agent staged, "" when the
// staging predates versions (pods then mount the legacy tree).
func (a *Agent) currentBundle() string {
	b, err := os.ReadFile(filepath.Join(a.bundleRoot(), "current-bundle"))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !validBundleID(id) {
		return ""
	}
	if _, err := os.Stat(filepath.Join(a.bundleVersionsDir(), id)); err != nil { //nolint:gosec // id passed validBundleID: hex, no separators
		return ""
	}
	return id
}

// validBundleID accepts the ids staging writes (an image version, or a
// content hash) and nothing that could name another path: letters,
// digits, '.', '_' and '-', not starting with '.'.
func validBundleID(id string) bool {
	if id == "" || len(id) > 128 || id[0] == '.' {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// podBundle is the bundle version a pod mounts, "" when unknown.
func (a *Agent) podBundle(ctx context.Context, ns, name string) string {
	if a.kubeClient == nil || ns == "" || name == "" {
		return ""
	}
	pod, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return webhook.PodBundleID(pod)
}

// bundleVersionMinAge keeps a version a while after it was staged or
// fetched: a pod admitted to mount it may not show yet.
const bundleVersionMinAge = time.Hour

// reapBundleVersions removes the bundle versions nothing on the node uses:
// not current, mounted by no pod of the node, recorded by no checkpoint on
// it, and older than bundleVersionMinAge. A staging or fetch left half
// done goes too, once as old.
func (a *Agent) reapBundleVersions(pods []corev1.Pod, now time.Time) {
	dir := a.bundleVersionsDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := map[string]bool{a.currentBundle(): true}
	for i := range pods {
		for _, id := range webhook.PodBundleIDs(&pods[i]) {
			keep[id] = true
		}
	}
	for id := range a.checkpointBundles() {
		keep[id] = true
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || !e.IsDir() || now.Sub(info.ModTime()) < bundleVersionMinAge {
			continue
		}
		if !strings.HasPrefix(e.Name(), ".") && keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			a.log.WithError(err).WithField("bundle", e.Name()).Warn("node reaper: cannot remove an unused bundle version")
			continue
		}
		a.log.WithField("bundle", e.Name()).Info("node reaper: removed an unused bundle version")
	}
}

// checkpointBundles are the bundle versions the checkpoints on this node
// recorded: their restores mount them.
func (a *Agent) checkpointBundles() map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(a.config.CheckpointDir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(a.config.CheckpointDir, e.Name(), "metadata.json"))
		if err != nil {
			continue
		}
		var md struct {
			Bundle string `json:"bundle"`
		}
		if json.Unmarshal(b, &md) == nil && validBundleID(md.Bundle) {
			out[md.Bundle] = true
		}
	}
	return out
}

// bundleHandler serves a bundle version of this node as a tar stream, for
// a peer whose restore needs it.
func (a *Agent) bundleHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if !validBundleID(id) {
		http.Error(w, "invalid bundle id", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(a.bundleVersionsDir(), id)
	if _, err := os.Stat(dir); err != nil { //nolint:gosec // id passed validBundleID: hex, no separators
		http.Error(w, "no such bundle version on this node", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	if err := writeBundleTar(w, dir); err != nil {
		a.log.WithError(err).WithField("bundle", id).Warn("serving a bundle version failed")
	}
}

// writeBundleTar writes the regular files, directories and symlinks under dir
// as a tar stream.
func writeBundleTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	// dir is a version the agent staged or fetched itself; nothing else
	// writes under it while it is served.
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error { //nolint:gosec // see above
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
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p) //nolint:gosec // the agent's own bundle version (see above)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// bundleMaxBytes bounds what a fetched bundle version may hold.
const bundleMaxBytes = 4 << 30

// readBundleTar extracts a tar stream written by writeBundleTar into dir.
// Every write goes through an os.Root of dir, so neither a name nor a
// symlink the stream itself creates can place a file outside it; an entry
// whose name leaves dir fails the extraction, as does a stream larger than
// bundleMaxBytes.
func readBundleTar(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("bundle entry %q leaves the bundle", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, tarPerm(hdr.Mode)|0o700); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += hdr.Size; hdr.Size < 0 || total > bundleMaxBytes {
				return fmt.Errorf("bundle larger than %d bytes", int64(bundleMaxBytes))
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, tarPerm(hdr.Mode))
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, tr, hdr.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

// tarPerm is the permission bits of a tar header's mode.
func tarPerm(mode int64) os.FileMode {
	if mode < 0 || mode > 0o777 {
		mode &= 0o777
	}
	return os.FileMode(uint32(mode)) & os.ModePerm //nolint:gosec // masked to 9 bits above
}

// ensurePlaceholderBundles fetches the bundle versions a pod scheduled to
// this node mounts and the node lacks: the pod cannot start without them
// (a restore placeholder mounts the version its checkpoint recorded).
func (c *criuAutoRestorer) ensurePlaceholderBundles(ctx context.Context, pod *corev1.Pod) {
	if pod.Spec.NodeName != c.a.config.NodeName || pod.Status.Phase != corev1.PodPending {
		return
	}
	for _, id := range webhook.PodBundleIDs(pod) {
		if !validBundleID(id) {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.a.bundleVersionsDir(), id)); err == nil {
			continue
		}
		key := "bundle/" + id
		if _, busy := c.attempted.LoadOrStore(key, struct{}{}); busy {
			continue
		}
		go func(id string) {
			defer c.attempted.Delete(key) // a later event retries a failure
			log := c.a.log.WithFields(logrus.Fields{"bundle": id, "pod": pod.Namespace + "/" + pod.Name})
			if err := c.a.fetchBundle(ctx, id); err != nil {
				log.WithError(err).Warn("cannot fetch the bundle version a pod needs; it waits")
				return
			}
			log.Info("fetched the bundle version a pod needs from a peer")
		}(id)
	}
}

// fetchBundle copies bundle version id from the first peer agent that
// has it.
func (a *Agent) fetchBundle(ctx context.Context, id string) error {
	pods, err := a.kubeClient.CoreV1().Pods(a.criuFailedNamespace()).List(ctx, metav1.ListOptions{LabelSelector: agentLabelSelector})
	if err != nil {
		return err
	}
	// A version holds one architecture's binaries: only a node of this
	// node's architecture has the right ones.
	arch := a.nodeArch(ctx, a.config.NodeName)
	var errs []error
	for i := range pods.Items {
		node := pods.Items[i].Spec.NodeName
		if node == "" || node == a.config.NodeName || a.nodeArch(ctx, node) != arch {
			continue
		}
		if err := a.fetchBundleFrom(ctx, node, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", node, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("no peer agent has bundle version %s: %w", id, errors.Join(errs...))
}

func (a *Agent) fetchBundleFrom(ctx context.Context, node, id string) error {
	base, err := a.peerAgentURL(ctx, node)
	if err != nil {
		return err
	}
	fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, base+"/v1/bundles/"+id, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := fabricHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	dir := a.bundleVersionsDir()
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		return merr
	}
	tmp, err := os.MkdirTemp(dir, "."+id+".fetch-")
	if err != nil {
		return err
	}
	if rerr := readBundleTar(resp.Body, tmp); rerr != nil {
		_ = os.RemoveAll(tmp)
		return rerr
	}
	// The peer is trusted for nothing: only contents with the digest the
	// staging agents recorded are installed.
	if verr := a.verifyBundle(fctx, id, a.nodeArch(fctx, a.config.NodeName), tmp); verr != nil {
		_ = os.RemoveAll(tmp)
		return verr
	}
	if cerr := os.Chmod(tmp, 0o755); cerr != nil {
		_ = os.RemoveAll(tmp)
		return cerr
	}
	if err := os.Rename(tmp, filepath.Join(dir, id)); err != nil {
		_ = os.RemoveAll(tmp)
		if _, serr := os.Stat(filepath.Join(dir, id)); serr == nil {
			return nil // another fetch won
		}
		return err
	}
	return nil
}

// nodeArch is a node's CPU architecture label, "" when unreadable.
func (a *Agent) nodeArch(ctx context.Context, node string) string {
	n, err := a.kubeClient.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return n.Labels[corev1.LabelArchStable]
}
