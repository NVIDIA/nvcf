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

package nvcf

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
)

func fakePSAT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(
		`{"sub":"system:serviceaccount:inst-1:nvcf-worker","aud":["nvcf-icms:cl-1"],"exp":` +
			strconv.FormatInt(exp, 10) + `}`))
	return header + "." + claims + ".fakesig"
}

// mountPSAT writes a fake PSAT under a temporary allowed root and points the env var at it.
func mountPSAT(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	old := token.MountedTokenRoot
	token.MountedTokenRoot = root + "/"
	t.Cleanup(func() { token.MountedTokenRoot = old })
	path := filepath.Join(root, "token")
	jwt := fakePSAT(time.Now().Add(15 * time.Minute).Unix())
	require.NoError(t, os.WriteFile(path, []byte(jwt), 0600))
	t.Setenv(token.MountedTokenPathEnvKey, path)
	return jwt
}

func TestCreateClient_NoMountedJWT_UsesBootstrap(t *testing.T) {
	t.Setenv(token.MountedTokenPathEnvKey, filepath.Join(t.TempDir(), "absent"))
	fqdn := startMockServer(t, &mockWorkerServer{})

	client, err := CreateClient(fqdn, nil, "bootstrap-token", nil, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.NoError(t, err)
	assert.False(t, client.delegatedToken)
	tok, err := client.NvcfTokenProvider.Token()
	require.NoError(t, err)
	assert.Equal(t, "bootstrap-token", tok.AccessToken)
}

func TestCreateClient_MountedJWT_RequiresHTTPS(t *testing.T) {
	mountPSAT(t)
	fqdn := startMockServer(t, &mockWorkerServer{}) // http://

	_, err := CreateClient(fqdn, nil, "bootstrap-token", nil, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires TLS")
}

func TestCreateClient_MountedJWT_PreferredOverBootstrapAndCache(t *testing.T) {
	jwt := mountPSAT(t)
	sharedDir := t.TempDir()
	require.NoError(t, token.CacheToken(filepath.Join(sharedDir, cachedNvcfTokenFilename),
		&oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(time.Hour)}))

	client, err := CreateClient("https://127.0.0.1:1", nil, "bootstrap-token", nil, "nca", "inst", "fn", "fnv", sharedDir, DefaultNvcfClientTimeout)
	require.NoError(t, err)
	assert.True(t, client.delegatedToken)
	tok, err := client.NvcfTokenProvider.Token()
	require.NoError(t, err)
	assert.Equal(t, jwt, tok.AccessToken, "mounted JWT wins over cached and bootstrap tokens")
}

func TestCreateClient_MountedJWT_UnreadableIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files")
	}
	mountPSAT(t)
	require.NoError(t, os.Chmod(os.Getenv(token.MountedTokenPathEnvKey), 0000))

	_, err := CreateClient("https://127.0.0.1:1", nil, "bootstrap-token", nil, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "no mounted JWT"), "read failures must not be treated as no token mounted")
}

// delegatedConnectClient answers ConnectOnce in-process. A real plaintext gRPC connection
// would reject the per-RPC credentials because a mounted JWT requires TLS.
type delegatedConnectClient struct {
	pb.WorkerClient
}

func (delegatedConnectClient) ConnectOnce(context.Context, *pb.WorkerConnect, ...grpc.CallOption) (*pb.WorkerConnectOnceResponse, error) {
	return &pb.WorkerConnectOnceResponse{
		ConnectedRegion: "us-east-1",
		NvcfWorkerToken: "", // NVCF issues no replacement token on the delegated path
		Expiration:      timestamppb.New(time.Now().Add(15 * time.Minute)),
	}, nil
}

func TestConnect_DelegatedToken_KeepsPSATAndPersistsNothing(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)
	c.Client = delegatedConnectClient{}
	c.delegatedToken = true
	before, err := c.NvcfTokenProvider.Token()
	require.NoError(t, err)

	require.NoError(t, c.connect(context.Background()))

	after, err := c.NvcfTokenProvider.Token()
	require.NoError(t, err)
	assert.Equal(t, before.AccessToken, after.AccessToken, "PSAT source must remain installed")
	_, statErr := os.Stat(filepath.Join(c.sharedConfigDir, cachedNvcfTokenFilename))
	assert.True(t, os.IsNotExist(statErr), "no token may be persisted on the delegated path")
	regions := c.ConnectedRegions.Load()
	require.NotNil(t, regions)
	assert.Equal(t, "us-east-1", regions.Primary)
}
