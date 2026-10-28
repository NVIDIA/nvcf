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

package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer wires up Store + Server backed by a temp dir
// and returns a live httptest.Server. Tests close it via t.Cleanup.
func newTestServer(t *testing.T) (string, *Store) {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(store).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, store
}

// shaHex helper.
func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestBlobPutGet_FullProtocolRoundTrip — the load-bearing test.
// Agent uploader (#58) PUTs a blob; restore-side cascade (#60
// extension) GETs it. Bytes must match. Status codes match the
// design doc: 201 on first PUT, 200 on duplicate, 200 on GET.
func TestBlobPutGet_FullProtocolRoundTrip(t *testing.T) {
	url, _ := newTestServer(t)
	body := []byte("PHASE5D-payload")
	sha := shaHex(body)

	// First PUT — 201 Created.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, url+"/v1/blob/"+sha, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("first PUT: status = %d, want 201", resp.StatusCode)
	}

	// Second PUT — 200 OK (idempotent).
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPut, url+"/v1/blob/"+sha, bytes.NewReader(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("second PUT: status = %d, want 200", resp.StatusCode)
	}

	// GET — 200 + body.
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/blob/"+sha, http.NoBody)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET: status = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("GET body = %q, want %q", got, body)
	}
}

// TestBlobHead_ExistsVsMissing — agent uploader uses HEAD to
// dedup ("don't re-upload what we already have"). 200 = skip
// upload, 404 = upload needed.
func TestBlobHead_ExistsVsMissing(t *testing.T) {
	url, store := newTestServer(t)
	body := []byte("present")
	sha := shaHex(body)
	if _, err := store.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodHead, url+"/v1/blob/"+sha, http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD existing: status = %d, want 200", resp.StatusCode)
	}

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodHead, url+"/v1/blob/"+strings.Repeat("0", 64), http.NoBody)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD missing: status = %d, want 404", resp.StatusCode)
	}
}

// TestBlobPut_HashMismatchIs400 — declared sha doesn't match
// payload. Server must reject with 400 (caller bug, not server
// problem) so the agent uploader can log + crash early instead
// of looping silently.
func TestBlobPut_HashMismatchIs400(t *testing.T) {
	url, _ := newTestServer(t)
	body := []byte("real bytes")
	wrongSha := strings.Repeat("a", 64)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, url+"/v1/blob/"+wrongSha, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (hash mismatch)", resp.StatusCode)
	}
}

// TestBlobGet_RangeRequest — receiver does parallel chunked
// download of large pages-*.img by issuing concurrent Range
// GETs. http.ServeContent must honor it (206 + correct slice).
func TestBlobGet_RangeRequest(t *testing.T) {
	url, store := newTestServer(t)
	body := []byte("0123456789ABCDEFGHIJ")
	sha := shaHex(body)
	if _, err := store.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/blob/"+sha, http.NoBody)
	req.Header.Set("Range", "bytes=5-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "56789" {
		t.Errorf("range body = %q, want %q", got, "56789")
	}
}

// TestBlobDelete_204AndIdempotent — GC sweep deletes blobs by
// sha. 204 on success; second delete also 204 (no row, no
// problem) so the sweep can be concurrent with retention sweeps.
func TestBlobDelete_204AndIdempotent(t *testing.T) {
	url, store := newTestServer(t)
	body := []byte("delete me")
	sha := shaHex(body)
	if _, err := store.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	for i, name := range []string{"first", "idempotent"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, url+"/v1/blob/"+sha, http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("delete[%d] %s: status = %d, want 204", i, name, resp.StatusCode)
		}
	}
}

// TestManifestPutGet_RoundTrip — the routing call. Agent uploads
// a manifest after blob upload; restore-side fetches it to drive
// per-file blob downloads.
func TestManifestPutGet_RoundTrip(t *testing.T) {
	url, _ := newTestServer(t)
	hash := "vllm-small__nvsnap-system__20260509-192743"
	want := Manifest{Files: []ManifestFile{
		{Path: "inventory.img", SHA256: strings.Repeat("a", 64), Size: 99},
		{Path: "rootfs-diff/etc/hostname", SHA256: strings.Repeat("b", 64), Size: 11},
	}}

	buf, _ := json.Marshal(want)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		url+"/v1/capture/"+hash+"/manifest.json", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("PUT manifest: status = %d, want 204", resp.StatusCode)
	}

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/capture/"+hash+"/manifest.json", http.NoBody)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET manifest: status = %d", resp.StatusCode)
	}
	var got Manifest
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Files[1].Path != "rootfs-diff/etc/hostname" {
		t.Errorf("got = %+v", got)
	}
}

// TestManifestPut_BadJsonIs400 — malformed JSON or violation of
// path constraints (absolute, ..) → 400, not 500. Caller bug
// shouldn't take down the server.
func TestManifestPut_BadJsonIs400(t *testing.T) {
	url, _ := newTestServer(t)
	cases := []struct {
		name, body string
	}{
		{"not-json", `{not json`},
		{"absolute-path", `{"files":[{"path":"/etc/passwd","sha256":"` + strings.Repeat("a", 64) + `","size":1}]}`},
		{"dotdot", `{"files":[{"path":"../escape","sha256":"` + strings.Repeat("a", 64) + `","size":1}]}`},
		{"bad-sha", `{"files":[{"path":"x","sha256":"deadbeef","size":1}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
				"https://example.com/v1/capture/x/manifest.json", strings.NewReader(tc.body))
			req.URL.Host = strings.TrimPrefix(url, "http://")
			req.URL.Scheme = "http"
			req.Host = req.URL.Host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

// TestManifestGet_NotFound — restore cascade asks for a capture
// the blob store doesn't have. 404, not 500 — cascade falls
// through to "checkpoint completely lost" which is a different
// failure mode the receiver surfaces to the user.
func TestManifestGet_NotFound(t *testing.T) {
	url, _ := newTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/capture/ghost/manifest.json", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestDeleteCapture_204 — admin lifecycle path. Removes manifest
// only; does NOT touch shared blobs (TestDeleteCapture_DoesNotTouchBlobs
// covers the store-level invariant).
func TestDeleteCapture_204(t *testing.T) {
	url, store := newTestServer(t)
	hash := "victim"
	if err := store.PutManifest(hash, &Manifest{Files: []ManifestFile{
		{Path: "x", SHA256: strings.Repeat("a", 64), Size: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, url+"/v1/capture/"+hash, http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/capture/"+hash+"/manifest.json", http.NoBody)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want 404", resp.StatusCode)
	}
}

// TestHealthz_DiskStats — operator-facing endpoint used by the
// k8s liveness probe + dashboards. Returns disk usage so a
// near-full PVC is visible before puts start ENOSPC'ing.
func TestHealthz_DiskStats(t *testing.T) {
	url, _ := newTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/healthz", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if got.Disk == nil || got.Disk.TotalBytes == 0 {
		t.Errorf("disk stats missing or zero: %+v", got.Disk)
	}
}

// TestBlobGet_InvalidShaIs400 — receiver bug or scan probe;
// 400 (not 404 or 500) is the right answer because it tells the
// caller "your input is malformed".
func TestBlobGet_InvalidShaIs400(t *testing.T) {
	url, _ := newTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/v1/blob/not-a-sha", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
