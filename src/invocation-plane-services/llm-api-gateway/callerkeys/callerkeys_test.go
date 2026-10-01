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

package callerkeys

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// SHA-256 of the plain keys "demo-ui-key" and "laptop-key".
const (
	demoUIKeyHash = "276932c4694447817ad43a6afceb8f8a64657038679602b46ce8dc254b18bbcd"
	laptopKeyHash = "9b7b36061a684541007d2c543574a1db801bc8f41f85ac5fdc0155fe72a9ec38"
)

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caller-keys.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoad_ValidFile_LooksUpKeyIDByPlainKey(t *testing.T) {
	t.Parallel()

	path := writeKeyFile(t, `keys:
  - id: demo-ui
    sha256: `+demoUIKeyHash+`
  - id: presenter-laptop
    sha256: `+laptopKeyHash+`
`)
	keys, err := Load(context.Background(), NewFileStore(path))
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		apiKey string
		wantID string
		wantOK bool
	}{
		{"first key", "demo-ui-key", "demo-ui", true},
		{"second key", "laptop-key", "presenter-laptop", true},
		{"unknown key", "other-key", "", false},
		{"empty key", "", "", false},
		{"hash presented as key", demoUIKeyHash, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id, ok := keys.Lookup(tc.apiKey)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantID, id)
		})
	}
}

func TestLoad_InvalidFile_FailsToLoad(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "duplicate id",
			content: "keys:\n" +
				"  - {id: demo-ui, sha256: " + demoUIKeyHash + "}\n" +
				"  - {id: demo-ui, sha256: " + laptopKeyHash + "}\n",
			wantErr: `duplicate caller key id "demo-ui"`,
		},
		{
			name: "duplicate hash",
			content: "keys:\n" +
				"  - {id: demo-ui, sha256: " + demoUIKeyHash + "}\n" +
				"  - {id: laptop, sha256: " + demoUIKeyHash + "}\n",
			wantErr: `caller key "laptop" repeats the sha256 of another key`,
		},
		{
			name:    "short hash",
			content: "keys:\n  - {id: demo-ui, sha256: " + demoUIKeyHash[:62] + "}\n",
			wantErr: `caller key "demo-ui": sha256 is not 64 hex characters`,
		},
		{
			name:    "long hash",
			content: "keys:\n  - {id: demo-ui, sha256: " + demoUIKeyHash + "00}\n",
			wantErr: `caller key "demo-ui": sha256 is not 64 hex characters`,
		},
		{
			name:    "non-hex hash",
			content: "keys:\n  - {id: demo-ui, sha256: " + "zz" + demoUIKeyHash[2:] + "}\n",
			wantErr: `caller key "demo-ui": sha256 is not 64 hex characters`,
		},
		{
			name:    "empty id",
			content: "keys:\n  - {id: '', sha256: " + demoUIKeyHash + "}\n",
			wantErr: "caller key 1: id is required",
		},
		{
			name:    "unknown field",
			content: "keys:\n  - {id: demo-ui, sha256: " + demoUIKeyHash + ", key: demo-ui-key}\n",
			wantErr: "field key not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(context.Background(), NewFileStore(writeKeyFile(t, tc.content)))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestLoad_MissingFile_FailsToLoad(t *testing.T) {
	t.Parallel()

	_, err := Load(context.Background(), NewFileStore(filepath.Join(t.TempDir(), "absent.yaml")))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// syncBuffer collects log output written by the refresh goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// replaceKeyFile swaps the file in one rename, as Kubernetes swaps a projected
// Secret, so a refresh never reads a half-written file.
func replaceKeyFile(t *testing.T, path string, content string) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o600))
	require.NoError(t, os.Rename(tmp, path))
}

// startRefresh loads the demo-ui key from a new file and refreshes it every
// few milliseconds until the test ends.
func startRefresh(t *testing.T) (*KeySet, string, *syncBuffer) {
	t.Helper()
	path := writeKeyFile(t, "keys:\n  - {id: demo-ui, sha256: "+demoUIKeyHash+"}\n")
	store := NewFileStore(path)
	keys, err := Load(context.Background(), store)
	require.NoError(t, err)

	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(zerolog.New(logs).WithContext(context.Background()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		keys.Refresh(ctx, store, 5*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return keys, path, logs
}

func TestKeySetRefresh_ValidFileChange_AppliesWithoutRestart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		newContent string
		apiKey     string
		wantOK     bool
	}{
		{
			name: "added key becomes valid",
			newContent: "keys:\n" +
				"  - {id: demo-ui, sha256: " + demoUIKeyHash + "}\n" +
				"  - {id: laptop, sha256: " + laptopKeyHash + "}\n",
			apiKey: "laptop-key",
			wantOK: true,
		},
		{
			name:       "removed key becomes invalid",
			newContent: "keys:\n  - {id: laptop, sha256: " + laptopKeyHash + "}\n",
			apiKey:     "demo-ui-key",
			wantOK:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys, path, logs := startRefresh(t)
			replaceKeyFile(t, path, tc.newContent)

			require.Eventually(t, func() bool {
				_, ok := keys.Lookup(tc.apiKey)
				return ok == tc.wantOK
			}, 5*time.Second, 5*time.Millisecond)
			require.Contains(t, logs.String(), `"message":"caller keys changed"`)
		})
	}
}

func TestKeySetRefresh_UnchangedFile_LogsNothing(t *testing.T) {
	t.Parallel()

	keys, _, logs := startRefresh(t)
	// Let several refreshes run.
	time.Sleep(50 * time.Millisecond)

	_, ok := keys.Lookup("demo-ui-key")
	require.True(t, ok)
	require.Empty(t, logs.String())
}

func TestKeySetLookup_ZeroValue_RejectsKey(t *testing.T) {
	t.Parallel()

	var keys KeySet
	_, ok := keys.Lookup("demo-ui-key")
	require.False(t, ok)
}

func TestKeySetRefresh_InvalidFile_KeepsLastGoodSetAndLogsError(t *testing.T) {
	t.Parallel()

	// Each file also lists the laptop key, which must not become valid.
	for _, tc := range []struct {
		name       string
		newContent string
		wantLog    string
	}{
		{
			name:       "malformed yaml",
			newContent: "keys: [{id: laptop, sha256: " + laptopKeyHash + "}\n",
			wantLog:    "parse caller key file",
		},
		{
			name: "duplicate id",
			newContent: "keys:\n" +
				"  - {id: laptop, sha256: " + demoUIKeyHash + "}\n" +
				"  - {id: laptop, sha256: " + laptopKeyHash + "}\n",
			wantLog: `duplicate caller key id \"laptop\"`,
		},
		{
			name: "duplicate hash",
			newContent: "keys:\n" +
				"  - {id: demo-ui, sha256: " + laptopKeyHash + "}\n" +
				"  - {id: laptop, sha256: " + laptopKeyHash + "}\n",
			wantLog: "repeats the sha256 of another key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys, path, logs := startRefresh(t)
			replaceKeyFile(t, path, tc.newContent)

			require.Eventually(t, func() bool {
				return strings.Contains(logs.String(), tc.wantLog)
			}, 5*time.Second, 5*time.Millisecond)
			require.Contains(t, logs.String(), `"level":"error"`)

			id, ok := keys.Lookup("demo-ui-key")
			require.True(t, ok)
			require.Equal(t, "demo-ui", id)
			_, ok = keys.Lookup("laptop-key")
			require.False(t, ok)
		})
	}
}
