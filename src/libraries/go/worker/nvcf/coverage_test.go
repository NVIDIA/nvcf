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
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/consumer"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
)

// ---------------------------------------------------------------------------
// In-process mock gRPC Worker server bound to an ephemeral port.
// ---------------------------------------------------------------------------

// mockWorkerServer is a configurable in-process implementation of the NVCF
// Worker gRPC service. Each handler delegates to an injectable function so a
// test can shape success/error behavior per RPC. All handlers default to
// Unimplemented via the embedded pb.UnimplementedWorkerServer.
type mockWorkerServer struct {
	pb.UnimplementedWorkerServer

	connectOnce      func(context.Context, *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error)
	secretCreds      func(context.Context, *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error)
	metadataCreds    func(context.Context, *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error)
	streamArtifacts  func(*pb.ArtifactsRequest, grpc.ServerStreamingServer[pb.StreamedArtifactFile]) error
	lastConnectReq   atomic.Pointer[pb.WorkerConnect]
	connectCallCount atomic.Int32
}

func (m *mockWorkerServer) ConnectOnce(ctx context.Context, req *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
	m.connectCallCount.Add(1)
	m.lastConnectReq.Store(req)
	if m.connectOnce == nil {
		return nil, status.Error(codes.Unimplemented, "connectOnce not configured")
	}
	return m.connectOnce(ctx, req)
}

func (m *mockWorkerServer) RequestSecretCredentials(ctx context.Context, req *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
	if m.secretCreds == nil {
		return nil, status.Error(codes.Unimplemented, "secretCreds not configured")
	}
	return m.secretCreds(ctx, req)
}

func (m *mockWorkerServer) RequestFunctionMetadataCredentials(ctx context.Context, req *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error) {
	if m.metadataCreds == nil {
		return nil, status.Error(codes.Unimplemented, "metadataCreds not configured")
	}
	return m.metadataCreds(ctx, req)
}

func (m *mockWorkerServer) StreamArtifacts(req *pb.ArtifactsRequest, stream grpc.ServerStreamingServer[pb.StreamedArtifactFile]) error {
	if m.streamArtifacts == nil {
		return status.Error(codes.Unimplemented, "streamArtifacts not configured")
	}
	return m.streamArtifacts(req, stream)
}

// startMockServer stands up the mock on 127.0.0.1:0 (ephemeral) and returns the
// http:// FQDN that CreateClient/createWorkerClient expect. The server is shut
// down via t.Cleanup.
func startMockServer(t *testing.T, mock *mockWorkerServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	pb.RegisterWorkerServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	return "http://" + lis.Addr().String()
}

// newTestClient builds a Client wired to the mock server's worker client with a
// static token provider. It avoids CreateClient's filesystem/NATS side effects.
// newConfigDir returns a temp directory for the client's shared config and
// removes it on cleanup with a brief retry. connect() caches the worker token
// to this directory from a background goroutine, so a plain t.TempDir()
// RemoveAll can race that final write and fail with "directory not empty".
// Retrying RemoveAll lets the in-flight write finish first.
func newConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nvcf-test-")
	require.NoError(t, err)
	t.Cleanup(func() {
		for i := 0; i < 100; i++ {
			if rmErr := os.RemoveAll(dir); rmErr == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

func newTestClient(t *testing.T, fqdn string) *Client {
	t.Helper()
	workerClient, err := createWorkerClient(fqdn, DefaultNvcfClientTimeout)
	require.NoError(t, err)
	return &Client{
		Client: workerClient,
		regionalNvcfClients: map[string]pb.WorkerClient{
			fqdn: workerClient,
		},
		NvcfTokenProvider: auth.NewSettableTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "test-token",
		})),
		ncaId:             "nca-1",
		instanceId:        "instance-1",
		functionId:        "fn-1",
		functionVersionId: "fnv-1",
		sharedConfigDir:   newConfigDir(t),
		clientTimeout:     DefaultNvcfClientTimeout,
	}
}

// ---------------------------------------------------------------------------
// createWorkerClient / CreateClient
// ---------------------------------------------------------------------------

func TestCreateWorkerClient_Success(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	client, err := createWorkerClient(fqdn, DefaultNvcfClientTimeout)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestCreateWorkerClient_InvalidURL(t *testing.T) {
	// A control character makes url.Parse fail inside PortSafeUrl.
	_, err := createWorkerClient("http://\x7f:badport", DefaultNvcfClientTimeout)
	require.Error(t, err)
}

func TestCreateClient_Success_CreatesSharedConfigDir(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	// Point at a not-yet-existing directory so the create branch runs.
	sharedDir := filepath.Join(t.TempDir(), "nested", "config")

	client, err := CreateClient(fqdn, nil, "env-token", nil, "nca", "inst", "fn", "fnv", sharedDir, DefaultNvcfClientTimeout)
	require.NoError(t, err)
	require.NotNil(t, client)

	// Directory should now exist.
	info, statErr := os.Stat(sharedDir)
	require.NoError(t, statErr)
	require.True(t, info.IsDir())

	// Token provider should serve the environment token (no cache present).
	tok, err := client.NvcfTokenProvider.Token()
	require.NoError(t, err)
	assert.Equal(t, "env-token", tok.AccessToken)

	client.Close() // no NatsConn, exercises the nil-guard path
}

func TestCreateClient_LoadsCachedToken(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	sharedDir := t.TempDir()

	// Pre-seed a cached token file.
	cached := &oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, token.CacheToken(filepath.Join(sharedDir, cachedNvcfTokenFilename), cached))

	client, err := CreateClient(fqdn, nil, "env-token", nil, "nca", "inst", "fn", "fnv", sharedDir, DefaultNvcfClientTimeout)
	require.NoError(t, err)

	tok, err := client.NvcfTokenProvider.Token()
	require.NoError(t, err)
	assert.Equal(t, "cached-token", tok.AccessToken)
}

func TestCreateClient_InvalidFqdn(t *testing.T) {
	_, err := CreateClient("http://\x7f:badport", nil, "tok", nil, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetRegionalNvcfClient
// ---------------------------------------------------------------------------

func TestGetRegionalNvcfClient(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)

	// Empty fqdn returns the default client.
	def, err := c.GetRegionalNvcfClient("")
	require.NoError(t, err)
	assert.Equal(t, c.Client, def)

	// Already-cached fqdn returns the cached client.
	cached, err := c.GetRegionalNvcfClient(fqdn)
	require.NoError(t, err)
	assert.Equal(t, c.Client, cached)

	// New fqdn creates and caches a new client.
	other := startMockServer(t, &mockWorkerServer{})
	created, err := c.GetRegionalNvcfClient(other)
	require.NoError(t, err)
	require.NotNil(t, created)
	// Second lookup hits the cache (double-checked lock path).
	again, err := c.GetRegionalNvcfClient(other)
	require.NoError(t, err)
	assert.Equal(t, created, again)

	// Invalid fqdn surfaces the createWorkerClient error.
	_, err = c.GetRegionalNvcfClient("http://\x7f:badport")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// connect / ConnectIndefinitely
// ---------------------------------------------------------------------------

func validConnectResponse() *pb.WorkerConnectOnceResponse {
	return &pb.WorkerConnectOnceResponse{
		ConnectedRegion: "us-east-1",
		OtherRegions:    []string{"us-west-2"},
		NvcfWorkerToken: "refreshed-token",
		Expiration:      timestamppb.New(time.Now().Add(time.Hour)),
	}
}

func TestConnect_Success(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return validConnectResponse(), nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	require.NoError(t, c.connect(context.Background()))

	// Token provider should now serve the refreshed token.
	require.Eventually(t, func() bool {
		tok, err := c.NvcfTokenProvider.Token()
		return err == nil && tok.AccessToken == "refreshed-token"
	}, 2*time.Second, 5*time.Millisecond)

	regions := c.ConnectedRegions.Load()
	require.NotNil(t, regions)
	assert.Equal(t, "us-east-1", regions.Primary)
	assert.Equal(t, []string{"us-west-2"}, regions.Secondaries)

	// The connect request carried the client identity.
	req := mock.lastConnectReq.Load()
	require.NotNil(t, req)
	assert.Equal(t, "instance-1", req.InstanceId)
	assert.Equal(t, "fn-1", req.FunctionId)
	assert.Equal(t, "fnv-1", req.FunctionVersionId)
}

func TestConnect_ServerError(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return nil, status.Error(codes.Unavailable, "boom")
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	err := c.connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to send connect request")
}

func TestConnect_EmptyRegion(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return &pb.WorkerConnectOnceResponse{ConnectedRegion: ""}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	err := c.connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connected region")
}

func TestConnect_InvalidToken(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return &pb.WorkerConnectOnceResponse{
				ConnectedRegion: "us-east-1",
				NvcfWorkerToken: "", // empty + zero expiry => invalid token
				Expiration:      timestamppb.New(time.Time{}),
			}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	err := c.connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid worker token")
}

func TestConnect_ContextAlreadyCancelled(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.connect(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestConnectIndefinitely_Success(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return validConnectResponse(), nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	parentCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx, err := c.ConnectIndefinitely(parentCtx)
	require.NoError(t, err)
	require.NotNil(t, ctx)

	// First connect must have succeeded.
	require.Eventually(t, func() bool {
		return mock.connectCallCount.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	// Stop the background reconnect loop so it cannot write to the shared config
	// dir while the test's temp dir is being cleaned up.
	cancel()
	<-ctx.Done()
}

func TestConnectIndefinitely_InitialFailureReturnsError(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return nil, status.Error(codes.Unavailable, "down")
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	// Cancel quickly so the bounded backoff returns the context error fast
	// instead of running its full 5-minute elapsed budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	retCtx, err := c.ConnectIndefinitely(ctx)
	require.Error(t, err)
	// Returned context should be done since cancelFunc was invoked.
	require.NotNil(t, retCtx)
	select {
	case <-retCtx.Done():
	default:
		t.Fatal("expected returned context to be cancelled")
	}
}

func TestConnectIndefinitely_BackgroundLoopExitsOnCancel(t *testing.T) {
	mock := &mockWorkerServer{
		connectOnce: func(_ context.Context, _ *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
			return validConnectResponse(), nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	parent, cancel := context.WithCancel(context.Background())
	ctx, err := c.ConnectIndefinitely(parent)
	require.NoError(t, err)

	// Cancel the parent; the background reconnect goroutine should observe it
	// and exit, which cancels the derived ctx (defer cancelFunc()).
	cancel()
	require.Eventually(t, func() bool {
		return ctx.Err() != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// ---------------------------------------------------------------------------
// GetArtifacts (server-streaming)
// ---------------------------------------------------------------------------

func TestGetArtifacts_Success(t *testing.T) {
	mock := &mockWorkerServer{
		streamArtifacts: func(_ *pb.ArtifactsRequest, stream grpc.ServerStreamingServer[pb.StreamedArtifactFile]) error {
			files := []*pb.StreamedArtifactFile{
				{ArtifactName: "model-a", ArtifactVersion: "1", Path: "/m/a", Url: "http://a", ArtifactKind: pb.StreamedArtifactFile_MODEL},
				{ArtifactName: "res-b", ArtifactVersion: "2", Path: "/r/b", Url: "http://b", ArtifactKind: pb.StreamedArtifactFile_RESOURCE},
				{ArtifactName: "bad-c", ArtifactKind: pb.StreamedArtifactFile_ArtifactKindEnum(99)}, // invalid kind
			}
			for _, f := range files {
				if err := stream.Send(f); err != nil {
					return err
				}
			}
			return nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	list, err := c.GetArtifacts(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Models, 1)
	require.Len(t, list.Resources, 1)
	assert.Equal(t, "model-a", list.Models[0].Name)
	assert.Equal(t, "res-b", list.Resources[0].Name)
}

func TestGetArtifacts_StreamStartError(t *testing.T) {
	// streamArtifacts not configured -> Unimplemented; backoff retries then fails.
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel so backoff stops immediately

	_, err := c.GetArtifacts(ctx)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// getAssertionToken / writeAssertionTokenToDisk (ess.go)
// ---------------------------------------------------------------------------

func TestGetAssertionToken_Success(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	mock := &mockWorkerServer{
		secretCreds: func(_ context.Context, _ *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
			return &pb.SecretCredentialsResponse{
				SecretCredentialsToken: "secret-tok",
				Expiration:             timestamppb.New(exp),
			}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	tok, err := c.getAssertionToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "secret-tok", tok.Token)
	assert.WithinDuration(t, exp, tok.Expiration, time.Second)
}

func TestGetAssertionToken_ContextCancelled(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.getAssertionToken(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetAssertionToken_ServerError(t *testing.T) {
	mock := &mockWorkerServer{
		secretCreds: func(_ context.Context, _ *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "nope")
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // make backoff stop after the first failed attempt
	_, err := c.getAssertionToken(ctx)
	require.Error(t, err)
}

func TestWriteAssertionTokenToDisk(t *testing.T) {
	c := &Client{}
	dir := filepath.Join(t.TempDir(), "ess", "nested")
	c.assertionTokenPath = filepath.Join(dir, "assertion.token")

	err := c.writeAssertionTokenToDisk(token.Token{Token: "abc123"})
	require.NoError(t, err)

	data, err := os.ReadFile(c.assertionTokenPath)
	require.NoError(t, err)
	assert.Equal(t, "abc123", string(data))

	// Writing again when the dir already exists exercises the stat-exists path.
	require.NoError(t, c.writeAssertionTokenToDisk(token.Token{Token: "def456"}))
	data, err = os.ReadFile(c.assertionTokenPath)
	require.NoError(t, err)
	assert.Equal(t, "def456", string(data))
}

func TestWriteAssertionTokenToDisk_DirCreateError(t *testing.T) {
	// Make a regular file and try to use it as a parent directory component.
	base := t.TempDir()
	fileAsDir := filepath.Join(base, "afile")
	require.NoError(t, os.WriteFile(fileAsDir, []byte("x"), 0600))

	c := &Client{assertionTokenPath: filepath.Join(fileAsDir, "sub", "token")}
	err := c.writeAssertionTokenToDisk(token.Token{Token: "x"})
	require.Error(t, err)
}

func TestMetadataCredentialsCallback_DirCreateError(t *testing.T) {
	base := t.TempDir()
	fileAsDir := filepath.Join(base, "afile")
	require.NoError(t, os.WriteFile(fileAsDir, []byte("x"), 0600))

	orig := MetadataCredsTokenFile
	MetadataCredsTokenFile = filepath.Join(fileAsDir, "sub", "self")
	defer func() { MetadataCredsTokenFile = orig }()

	c := &Client{}
	err := c.metadataCredentialsCallback(token.Token{Token: "x"})
	require.Error(t, err)
}

func TestStartAssertionTokenRefresher_StopsOnCancel(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	called := make(chan struct{}, 1)
	mock := &mockWorkerServer{
		secretCreds: func(_ context.Context, _ *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
			select {
			case called <- struct{}{}:
			default:
			}
			return &pb.SecretCredentialsResponse{
				SecretCredentialsToken: "tok",
				Expiration:             timestamppb.New(exp),
			}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	dir := newConfigDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartAssertionTokenRefresher(ctx, filepath.Join(dir, "assertion.token"), false)

	// Wait for at least one refresh cycle to run, then cancel.
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("assertion refresher never called the RPC")
	}
	cancel()
}

// ---------------------------------------------------------------------------
// getMetadataCredentials / metadataCredentialsCallback (metadata.go)
// ---------------------------------------------------------------------------

func TestGetMetadataCredentials_Success(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	mock := &mockWorkerServer{
		metadataCreds: func(_ context.Context, req *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error) {
			assert.Equal(t, "nca-1", req.NcaId)
			assert.Equal(t, "fn-1", req.FunctionId)
			return &pb.FunctionMetadataCredentialsResponse{
				FunctionMetadataCredentialsToken: "meta-tok",
				Expiration:                       timestamppb.New(exp),
			}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	tok, err := c.getMetadataCredentials(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "meta-tok", tok.Token)
}

func TestGetMetadataCredentials_ContextCancelled(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	c := newTestClient(t, fqdn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.getMetadataCredentials(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetMetadataCredentials_ServerError(t *testing.T) {
	mock := &mockWorkerServer{
		metadataCreds: func(_ context.Context, _ *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error) {
			return nil, status.Error(codes.Internal, "boom")
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.getMetadataCredentials(ctx)
	require.Error(t, err)
}

func TestMetadataCredentialsCallback(t *testing.T) {
	c := &Client{}
	dir := filepath.Join(t.TempDir(), "info")

	// Override the package-level path for the duration of the test.
	orig := MetadataCredsTokenFile
	MetadataCredsTokenFile = filepath.Join(dir, "self")
	defer func() { MetadataCredsTokenFile = orig }()

	require.NoError(t, c.metadataCredentialsCallback(token.Token{Token: "metadata-payload"}))
	data, err := os.ReadFile(MetadataCredsTokenFile)
	require.NoError(t, err)
	assert.Equal(t, "metadata-payload", string(data))

	// Second write exercises the existing-directory branch.
	require.NoError(t, c.metadataCredentialsCallback(token.Token{Token: "second"}))
	data, err = os.ReadFile(MetadataCredsTokenFile)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))
}

func TestStartMetadataCredentialsRefresher_StopsOnCancel(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	called := make(chan struct{}, 1)
	mock := &mockWorkerServer{
		metadataCreds: func(_ context.Context, _ *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error) {
			select {
			case called <- struct{}{}:
			default:
			}
			return &pb.FunctionMetadataCredentialsResponse{
				FunctionMetadataCredentialsToken: "meta",
				Expiration:                       timestamppb.New(exp),
			}, nil
		},
	}
	fqdn := startMockServer(t, mock)
	c := newTestClient(t, fqdn)

	dir := t.TempDir()
	orig := MetadataCredsTokenFile
	MetadataCredsTokenFile = filepath.Join(dir, "self")
	defer func() { MetadataCredsTokenFile = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartMetadataCredentialsRefresher(ctx)

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata refresher never called the RPC")
	}
	cancel()
}

// ---------------------------------------------------------------------------
// doneCtx and KeepAliveInvokeFunctionRequest (client.go)
// ---------------------------------------------------------------------------

func TestDoneCtx(t *testing.T) {
	// Timeout reached before context done -> false.
	assert.False(t, doneCtx(context.Background(), time.Millisecond))

	// Context already done -> true (and the timer is stopped cleanly).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.True(t, doneCtx(ctx, time.Hour))
}

// fakeJetstreamMsg is a minimal jetstream.Msg whose InProgress is controllable.
type fakeJetstreamMsg struct {
	jetstream.Msg
	inProgressErr error
	calls         atomic.Int32
}

func (f *fakeJetstreamMsg) InProgress() error {
	f.calls.Add(1)
	return f.inProgressErr
}

func newWorkRequest(msg jetstream.Msg) *consumer.WorkRequest {
	return &consumer.WorkRequest{Msg: msg, RequestData: pb.WorkerInvokeFunctionRequest{RequestId: "req-1"}}
}

func TestKeepAliveInvokeFunctionRequest_StopsWhenContextDone(t *testing.T) {
	// keepAlivePeriod is 10s; with an already-cancelled context the loop's first
	// doneCtx returns true immediately and the function returns without sending.
	msg := &fakeJetstreamMsg{}
	work := newWorkRequest(msg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		c := &Client{}
		c.KeepAliveInvokeFunctionRequest(ctx, work, func(error) {})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KeepAliveInvokeFunctionRequest did not return on cancelled context")
	}
	assert.Equal(t, int32(0), msg.calls.Load())
}

// ---------------------------------------------------------------------------
// updateConnectedRegions and metric helpers (client.go)
// ---------------------------------------------------------------------------

func TestUpdateConnectedRegions(t *testing.T) {
	c := &Client{}

	// Initial set: old == nil branch.
	c.updateConnectedRegions("us-east-1", []string{"us-west-2"})
	regions := c.ConnectedRegions.Load()
	require.NotNil(t, regions)
	assert.Equal(t, "us-east-1", regions.Primary)

	// Second set with no current consumer: currentConsumer == nil branch.
	c.updateConnectedRegions("us-east-2", []string{"eu-west-1"})
	regions = c.ConnectedRegions.Load()
	assert.Equal(t, "us-east-2", regions.Primary)
}

func TestUpdateConnectedRegions_WithConsumer(t *testing.T) {
	c := &Client{}
	// Seed an initial value so subsequent calls take the old != nil path.
	c.updateConnectedRegions("us-east-1", []string{"us-west-2"})

	// A zero-value consumer reports an empty currently-connected set, which is
	// safe to read; this drives the "consumer present" branch of the method.
	c.CurrentNatsConsumer.Store(&consumer.GlobalNatsConsumer{})

	triggered := make(chan struct{}, 1)
	trigger := func() {
		select {
		case triggered <- struct{}{}:
		default:
		}
	}
	c.TriggerNewNatsConsumer.Store(&trigger)

	// Change the primary region: this differs from the (empty) connected set,
	// so the method should decide to reload and invoke the trigger.
	c.updateConnectedRegions("eu-central-1", []string{"ap-south-1"})

	select {
	case <-triggered:
	case <-time.After(time.Second):
		t.Fatal("expected reload trigger to fire on region change")
	}

	// Calling again with the same primary but a region set that the empty
	// connected map does not match still triggers the secondary-region path.
	c.updateConnectedRegions("eu-central-1", []string{"ap-south-1", "ap-east-1"})
}

func TestSetConnectedSecondaryRegionsMetric(t *testing.T) {
	// Empty list -> "none" label branch.
	setConnectedSecondaryRegionsMetric(nil, 1)
	// Non-empty list -> sorted-join branch (also covers the copy/sort).
	setConnectedSecondaryRegionsMetric([]string{"b", "a"}, 1)
}

func TestSetConnectedPrimaryRegionsMetric(t *testing.T) {
	setConnectedPrimaryRegionsMetric("old-region", "new-region")
}

// ---------------------------------------------------------------------------
// natsAuthOption nkey error path not already covered: ensure FromSeed error.
// ---------------------------------------------------------------------------

func TestNatsAuthOption_NilNkeyUsesTokenHandler(t *testing.T) {
	tp := auth.NewSettableTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"}))
	opt, err := natsAuthOption(nil, tp)
	require.NoError(t, err)
	require.NotNil(t, opt)
}

// guard: ensure errors import is used even if some branches change.
var _ = errors.New

// ---------------------------------------------------------------------------
// newNatsConnection and Close against an embedded NATS server (ephemeral port).
// ---------------------------------------------------------------------------

func startEmbeddedNats(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{
		Host: "127.0.0.1",
		Port: -1, // -1 asks the server to pick an ephemeral port
	}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func TestNewNatsConnection_Success(t *testing.T) {
	url := startEmbeddedNats(t)
	tp := auth.NewSettableTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"}))

	nc, err := newNatsConnection(url, nil, tp, "inst-1", "fnv-1")
	require.NoError(t, err)
	require.NotNil(t, nc)

	// Exercise Client.Close with a live NatsConn.
	c := &Client{NatsConn: nc}
	c.Close()
}

func TestCreateClient_WithNats(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	natsURL := startEmbeddedNats(t)

	client, err := CreateClient(fqdn, &natsURL, "env-token", nil, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.NatsConn)
	require.NotNil(t, client.JetStream)

	// Close with a live NatsConn exercises the flush + close path.
	client.Close()
}

func TestCreateClient_NatsConnectError(t *testing.T) {
	fqdn := startMockServer(t, &mockWorkerServer{})
	// Invalid nkey seed makes natsAuthOption (inside newNatsConnection) fail.
	bad := "not-a-valid-seed"
	_, err := CreateClient(fqdn, ptr("nats://127.0.0.1:4222"), "tok", &bad, "nca", "inst", "fn", "fnv", t.TempDir(), DefaultNvcfClientTimeout)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to nats")
}

func ptr(s string) *string { return &s }

func TestNewNatsConnection_InvalidNkey(t *testing.T) {
	tp := auth.NewSettableTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"}))
	bad := "not-a-valid-seed"
	_, err := newNatsConnection("nats://127.0.0.1:4222", &bad, tp, "inst", "fnv")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// KeepAliveInvokeFunctionRequest: send a successful keepalive then exit.
// ---------------------------------------------------------------------------

// TestKeepAliveInvokeFunctionRequest_ReturnsOnCancelWithFailingMsg confirms the
// loop returns promptly when the context is cancelled, even when the underlying
// message keepalive would fail. The send branch itself runs on a fixed 10s
// period in production code, so a timed-send assertion is intentionally avoided
// to keep the test fast and non-flaky.
func TestKeepAliveInvokeFunctionRequest_ReturnsOnCancelWithFailingMsg(t *testing.T) {
	msg := &fakeJetstreamMsg{inProgressErr: errors.New("nope")}
	work := newWorkRequest(msg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		c := &Client{}
		c.KeepAliveInvokeFunctionRequest(ctx, work, func(error) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not return on cancelled context")
	}
}
