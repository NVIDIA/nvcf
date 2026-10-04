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

package nvct

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
)

// fakePSAT builds an unsigned projected ServiceAccount token shape with an exp claim.
func fakePSAT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(
		`{"sub":"system:serviceaccount:inst-1:nvcf-worker","aud":["nvcf-icms:cl-1"],"exp":` +
			strconv.FormatInt(exp, 10) + `}`))
	return header + "." + claims + ".fakesig"
}

// mountPSAT writes a JWT under a temporary allowed root and points the env var at it.
func mountPSAT(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := token.MountedTokenRoot
	token.MountedTokenRoot = root + "/"
	t.Cleanup(func() { token.MountedTokenRoot = old })
	jwt := fakePSAT(time.Now().Add(15 * time.Minute).Unix())
	path := filepath.Join(root, "token")
	if err := os.WriteFile(path, []byte(jwt), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(token.MountedTokenPathEnvKey, path)
	return jwt
}

func bootstrapJWT(t *testing.T) string {
	t.Helper()
	s, err := testutils.GenerateJWT(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateClient_NoMountedJWT_UsesBootstrap(t *testing.T) {
	t.Setenv(token.MountedTokenPathEnvKey, filepath.Join(t.TempDir(), "absent"))
	bootstrap := bootstrapJWT(t)

	c, err := CreateClient(nvctFQDN, bootstrap, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.delegatedToken {
		t.Error("delegatedToken should be false without a mounted JWT")
	}
	tok, err := c.NvctTokenProvider.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != bootstrap {
		t.Errorf("expected bootstrap token, got %q", tok.AccessToken)
	}
}

func TestCreateClient_MountedJWT_RequiresHTTPS(t *testing.T) {
	mountPSAT(t)
	_, err := CreateClient(nvctFQDN, bootstrapJWT(t), instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("expected TLS requirement error for http endpoint, got %v", err)
	}
}

func TestCreateClient_MountedJWT_Preferred(t *testing.T) {
	jwt := mountPSAT(t)
	c, err := CreateClient("https://localhost:9091", bootstrapJWT(t), instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.delegatedToken {
		t.Error("delegatedToken should be true with a mounted JWT")
	}
	tok, err := c.NvctTokenProvider.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != jwt {
		t.Errorf("expected mounted JWT, got %q", tok.AccessToken)
	}
}

func TestCreateClient_MountedJWT_UnreadableIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files")
	}
	mountPSAT(t)
	if err := os.Chmod(os.Getenv(token.MountedTokenPathEnvKey), 0000); err != nil {
		t.Fatal(err)
	}
	_, err := CreateClient("https://localhost:9091", bootstrapJWT(t), instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an unreadable mounted JWT")
	}
}

func TestStartWorkerTokenRefresher_DelegatedTokenIsNoop(t *testing.T) {
	jwt := mountPSAT(t)
	c, err := CreateClient("https://localhost:9091", bootstrapJWT(t), instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.StartWorkerTokenRefresher(ctx, false)
	time.Sleep(500 * time.Millisecond)

	tok, err := c.NvctTokenProvider.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != jwt {
		t.Errorf("refresher must not replace the mounted JWT; got %q", tok.AccessToken)
	}
	if _, statErr := os.Stat(filepath.Join(c.sharedConfigDir, cachedNvctTokenFilename)); !os.IsNotExist(statErr) {
		t.Error("no token may be persisted on the delegated path")
	}
}
