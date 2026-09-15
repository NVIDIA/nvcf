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
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
)

// ------------------------------------------------------------------------
// In-process, ephemeral-port gRPC mock with per-test configurable behavior.
// This avoids the shared fixed-port mock used by client_test.go and lets us
// exercise the error/edge branches of the nvct client.

type cfgMockServer struct {
	pb.UnimplementedWorkerServer

	connectRegion string
	connectErr    error
	connectCalls  atomic.Int32

	heartbeatErr error

	refreshToken string
	refreshErr   error

	secretToken  string
	secretExpiry time.Time
	secretErr    error

	artifacts    *pb.ArtifactsResponse
	artifactsErr error

	resultRecvErr error // returned by stream after first Recv
}

func (s *cfgMockServer) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	s.connectCalls.Add(1)
	if s.connectErr != nil {
		return nil, s.connectErr
	}
	return &pb.ConnectResponse{ConnectedRegion: s.connectRegion}, nil
}

func (s *cfgMockServer) SendHeartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	if s.heartbeatErr != nil {
		return nil, s.heartbeatErr
	}
	return &pb.HeartbeatResponse{
		TaskId:          req.GetTaskId(),
		ExecutionStatus: pb.ExecutionStatus_RUNNING.String(),
	}, nil
}

func (s *cfgMockServer) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.RefreshTokenResponse, error) {
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return &pb.RefreshTokenResponse{Token: s.refreshToken}, nil
}

func (s *cfgMockServer) RequestSecretCredentials(ctx context.Context, req *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
	if s.secretErr != nil {
		return nil, s.secretErr
	}
	return &pb.SecretCredentialsResponse{
		SecretCredentialsToken: s.secretToken,
		Expiration:             timestamppb.New(s.secretExpiry),
	}, nil
}

func (s *cfgMockServer) GetArtifacts(ctx context.Context, req *pb.ArtifactsRequest) (*pb.ArtifactsResponse, error) {
	if s.artifactsErr != nil {
		return nil, s.artifactsErr
	}
	return s.artifacts, nil
}

func (s *cfgMockServer) SendResultMetadata(stream pb.Worker_SendResultMetadataServer) error {
	for {
		_, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if s.resultRecvErr != nil {
			return s.resultRecvErr
		}
	}
	return stream.SendAndClose(&pb.ResultMetadataResponse{})
}

// startMock spins up the mock on 127.0.0.1:0 (ephemeral) and returns the
// "http://host:port" FQDN. It registers cleanup to gracefully stop.
func startMock(t *testing.T, srv *cfgMockServer) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	pb.RegisterWorkerServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.GracefulStop)

	return fmt.Sprintf("http://%s", lis.Addr().String())
}

// newClientForMock builds a real nvct.Client wired to the ephemeral mock.
func newClientForMock(t *testing.T, fqdn string) *Client {
	t.Helper()
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	require.NoError(t, err)
	c, err := CreateClient(fqdn, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	require.NoError(t, err)
	return c
}

// ------------------------------------------------------------------------
// calculateAssertionTokenExpiry: covers parse error and missing-iat branches
// plus the happy path expiry math.

func TestCalculateAssertionTokenExpiry(t *testing.T) {
	t.Run("valid token sets expiry iat+90m", func(t *testing.T) {
		iat := time.Now().Add(-10 * time.Minute).Round(time.Second)
		tok, err := testutils.GenerateJWT(iat.Unix())
		require.NoError(t, err)

		expiry, err := calculateAssertionTokenExpiry(tok)
		require.NoError(t, err)
		assert.Equal(t, iat.Add(expiryIntervalInMins*time.Minute).Unix(), expiry.Unix())
	})

	t.Run("malformed token returns parse error", func(t *testing.T) {
		_, err := calculateAssertionTokenExpiry("not-a-jwt")
		require.Error(t, err)
	})

	t.Run("empty token returns parse error", func(t *testing.T) {
		_, err := calculateAssertionTokenExpiry("")
		require.Error(t, err)
	})
}

// ------------------------------------------------------------------------
// CreateClient: malformed worker token -> token expiry failure branch.

func TestCreateClientInvalidToken(t *testing.T) {
	srv := &cfgMockServer{connectRegion: "localhost"}
	fqdn := startMock(t, srv)

	_, err := CreateClient(fqdn, "not-a-jwt", instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get token expiry")
}

// CreateClient: sharedConfigDir does not exist yet -> creation branch.
func TestCreateClientCreatesMissingDir(t *testing.T) {
	srv := &cfgMockServer{connectRegion: "localhost"}
	fqdn := startMock(t, srv)

	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	require.NoError(t, err)

	// Nested path that does not exist; triggers CreateDirectory branch.
	dir := t.TempDir() + "/created/nested"
	c, err := CreateClient(fqdn, tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, dir)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// CreateClient: invalid FQDN -> createWorkerClient error branch.
func TestCreateClientInvalidFqdn(t *testing.T) {
	tokenString, err := testutils.GenerateJWT(time.Now().Unix())
	require.NoError(t, err)
	_, err = CreateClient("://bad url", tokenString, instanceId, taskId, instanceType, DefaultNvctClientTimeout, t.TempDir())
	require.Error(t, err)
}

// ------------------------------------------------------------------------
// GetRegionalNvctClient: empty fqdn, cache hit, and new-client creation.

func TestGetRegionalNvctClient(t *testing.T) {
	srv := &cfgMockServer{connectRegion: "localhost"}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	t.Run("empty fqdn returns default client", func(t *testing.T) {
		got, err := c.GetRegionalNvctClient("")
		require.NoError(t, err)
		assert.Equal(t, c.Client, got)
	})

	t.Run("known fqdn returns cached client", func(t *testing.T) {
		got, err := c.GetRegionalNvctClient(fqdn)
		require.NoError(t, err)
		assert.Equal(t, c.Client, got)
	})

	t.Run("new fqdn creates and caches client", func(t *testing.T) {
		other := startMock(t, &cfgMockServer{connectRegion: "localhost"})
		got, err := c.GetRegionalNvctClient(other)
		require.NoError(t, err)
		require.NotNil(t, got)

		// Second call must hit the cache (same client instance returned).
		got2, err := c.GetRegionalNvctClient(other)
		require.NoError(t, err)
		assert.Equal(t, got, got2)
	})

	t.Run("invalid new fqdn returns error", func(t *testing.T) {
		_, err := c.GetRegionalNvctClient("://bad url")
		require.Error(t, err)
	})
}

// ------------------------------------------------------------------------
// Connect: empty connected region branch, and context-cancelled fast return.

func TestConnectEmptyRegion(t *testing.T) {
	srv := &cfgMockServer{connectRegion: ""}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	// backoff would retry forever on empty region, so bound it with a deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	err := c.Connect(ctx)
	require.Error(t, err)
}

func TestConnectContextAlreadyCancelled(t *testing.T) {
	srv := &cfgMockServer{connectRegion: "localhost"}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Connect(cctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), srv.connectCalls.Load())
}

// ------------------------------------------------------------------------
// SendInProgressHeartbeat: invalid status guard, and SendMaxRuntimeExceededHeartbeat.

func TestSendInProgressHeartbeatInvalidStatus(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	_, err := c.SendInProgressHeartbeat(context.Background(), pb.ExecutionStatus_COMPLETED)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
}

func TestSendInProgressHeartbeatInitializing(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	status, err := c.SendInProgressHeartbeat(context.Background(), pb.ExecutionStatus_TASK_CONTAINER_INITIALIZING)
	require.NoError(t, err)
	assert.Equal(t, pb.ExecutionStatus_RUNNING.String(), status)
}

func TestSendMaxRuntimeExceededHeartbeat(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	err := c.SendMaxRuntimeExceededHeartbeat(context.Background(), "ran too long")
	require.NoError(t, err)
}

// ------------------------------------------------------------------------
// getWorkerToken / getAssertionToken: context-cancelled guard returns ctx.Err().

func TestGetWorkerTokenContextCancelled(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.getWorkerToken(cctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetWorkerTokenInvalidServerToken(t *testing.T) {
	// Server returns a non-JWT token; calculateAssertionTokenExpiry should fail.
	srv := &cfgMockServer{refreshToken: "not-a-jwt"}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	_, err := c.getWorkerToken(context.Background())
	require.Error(t, err)
}

func TestGetAssertionTokenContextCancelled(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.getAssertionToken(cctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGetAssertionTokenSuccess(t *testing.T) {
	srv := &cfgMockServer{
		secretToken:  "secret-creds",
		secretExpiry: time.Now().Add(time.Hour),
	}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	tok, err := c.getAssertionToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "secret-creds", tok.Token)
}

// ------------------------------------------------------------------------
// writeAssertionTokenToDisk: creates a missing parent dir branch.

func TestWriteAssertionTokenToDiskCreatesDir(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	c.assertionTokenPath = t.TempDir() + "/new/sub/jwt.token"

	err := c.writeAssertionTokenToDisk(token.Token{Token: "hello-token"})
	require.NoError(t, err)
}

// ------------------------------------------------------------------------
// GetArtifacts: RESOURCE kind, invalid/unknown kind warn branch, empty response.

func TestGetArtifactsKinds(t *testing.T) {
	srv := &cfgMockServer{
		artifacts: &pb.ArtifactsResponse{
			Artifacts: []*pb.ArtifactsResponse_ArtifactResponse{
				{
					Name:    "res-1",
					Version: "1",
					Kind:    pb.ArtifactsResponse_ArtifactResponse_RESOURCE,
					Files: []*pb.ArtifactsResponse_ArtifactResponse_ArtifactFile{
						{Path: "data.bin", Url: "files/data.bin"},
					},
				},
				{
					Name:    "unknown-1",
					Version: "1",
					// An out-of-range enum value (MODEL=0, RESOURCE=1) hits the
					// default warn branch in GetArtifacts. The zero value maps to
					// MODEL, so we must use an explicitly unknown kind here.
					Kind: pb.ArtifactsResponse_ArtifactResponse_ArtifactKindEnum(99),
					Files: []*pb.ArtifactsResponse_ArtifactResponse_ArtifactFile{
						{Path: "x", Url: "y"},
					},
				},
			},
		},
	}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	list, err := c.GetArtifacts(context.Background())
	require.NoError(t, err)
	require.NotNil(t, list)
	assert.Len(t, list.Resources, 1)
	assert.Equal(t, "res-1", list.Resources[0].Name)
	assert.Empty(t, list.Models)
}

// ------------------------------------------------------------------------
// StreamingClient: Flush delegates to Close; Close with no streamer is a no-op;
// SendResult success populates lastMessageSent; getStreamer reuse.

func TestStreamingClientFlushNoStreamer(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	sc := NewStreamingClient(context.Background(), c.Client, c.NvctTokenProvider)
	// No streamer created yet; Flush -> Close should be a clean no-op.
	require.NoError(t, sc.Flush())
}

func TestStreamingClientSendAndFlush(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	sc := NewStreamingClient(context.Background(), c.Client, c.NvctTokenProvider)

	var progress uint32
	req := &pb.ResultMetadataRequest{
		TaskId:          taskId,
		InstanceId:      instanceId,
		InstanceType:    instanceType,
		Status:          pb.ExecutionStatus_IN_PROGRESS,
		PercentComplete: &progress,
	}
	require.NoError(t, sc.SendResult(req))
	// lastMessageSent should now be populated.
	require.NotNil(t, sc.lastMessageSent.Load())

	// getStreamer should reuse the existing streamer (RLock fast path).
	streamer, err := sc.getStreamer(context.Background())
	require.NoError(t, err)
	require.NotNil(t, streamer)

	// Flush flushes and closes.
	require.NoError(t, sc.Flush())
}

// clearOldStreamer: force a stale lastMessageSent so the streamer is torn down.
func TestStreamingClientClearOldStreamer(t *testing.T) {
	srv := &cfgMockServer{}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	sc := NewStreamingClient(context.Background(), c.Client, c.NvctTokenProvider)

	var progress uint32
	req := &pb.ResultMetadataRequest{
		TaskId:          taskId,
		InstanceId:      instanceId,
		InstanceType:    instanceType,
		Status:          pb.ExecutionStatus_IN_PROGRESS,
		PercentComplete: &progress,
	}
	require.NoError(t, sc.SendResult(req))

	// Backdate the last message so clearOldStreamer (>50s) tears the streamer down.
	old := time.Now().Add(-2 * time.Minute)
	sc.lastMessageSent.Store(&old)

	// getStreamer calls clearOldStreamer first, which closes the stale streamer,
	// then creates a fresh one.
	streamer, err := sc.getStreamer(context.Background())
	require.NoError(t, err)
	require.NotNil(t, streamer)

	require.NoError(t, sc.Close())
}

// SendResult after the server fails the stream should surface an error.
func TestStreamingClientSendResultServerError(t *testing.T) {
	srv := &cfgMockServer{resultRecvErr: errors.New("server rejected result")}
	fqdn := startMock(t, srv)
	c := newClientForMock(t, fqdn)

	sc := NewStreamingClient(context.Background(), c.Client, c.NvctTokenProvider)

	var progress uint32
	req := &pb.ResultMetadataRequest{
		TaskId:          taskId,
		InstanceId:      instanceId,
		InstanceType:    instanceType,
		Status:          pb.ExecutionStatus_IN_PROGRESS,
		PercentComplete: &progress,
	}

	// The first Send may succeed (buffered); Close should observe the server error.
	_ = sc.SendResult(req)
	err := sc.Close()
	require.Error(t, err)
}
