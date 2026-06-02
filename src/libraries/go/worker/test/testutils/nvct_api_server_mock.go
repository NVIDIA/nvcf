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

package testutils

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

// ------------------------------------------------------------------------

type MockNvctServer struct {
	pb.UnimplementedWorkerServer
	server             *grpc.Server
	taskId             string
	instanceId         string
	instanceType       string
	artifactServerUrl  string
	tokenRefreshPeriod time.Duration
}

// ------------------------------------------------------------------------

func NewMockNvctServer(taskId, instanceId, instanceType, artifactServerUrl string, tokenRefreshPeriod time.Duration) *MockNvctServer {
	s := &MockNvctServer{
		taskId:             taskId,
		instanceId:         instanceId,
		instanceType:       instanceType,
		artifactServerUrl:  artifactServerUrl,
		tokenRefreshPeriod: tokenRefreshPeriod,
	}

	s.server = grpc.NewServer()
	pb.RegisterWorkerServer(s.server, s)

	return s
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) Run(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	go func() {
		defer listener.Close()
		err := s.server.Serve(listener)
		if err != nil && err != grpc.ErrServerStopped {
			zap.L().Info("failed to serve", zap.Error(err))
			s.Shutdown()
		}
	}()

	return nil
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) Shutdown() {
	zap.L().Info("Shutting down mock nvcf server...")
	s.server.GracefulStop()
	zap.L().Info("Successfully shut down mock nvct server")
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) Connect(ctx context.Context, request *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	zap.L().Info("Got connect request")

	if err := s.validateWorkerInfo(request.GetTaskId(), request.GetInstanceId()); err != nil {
		return nil, err
	}

	response := &pb.ConnectResponse{
		ConnectedRegion: "localhost",
	}

	return response, nil
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) RefreshToken(ctx context.Context, request *pb.RefreshTokenRequest) (*pb.RefreshTokenResponse, error) {
	zap.L().Info("Got refresh token request")

	taskId := request.GetTaskId()
	if taskId != s.taskId {
		return nil, errors.New("invalid task id")
	}

	issuedAt := time.Now().Add(-90 * time.Minute).Round(time.Second).Add(s.tokenRefreshPeriod).Unix()
	jwtString, err := GenerateJWT(issuedAt)
	if err != nil {
		return nil, err
	}

	response := &pb.RefreshTokenResponse{
		Token: jwtString,
	}

	return response, nil
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) SendHeartbeat(ctx context.Context, request *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	zap.L().Info("Got send heart beat request")

	if err := s.validateWorkerInfo(request.GetTaskId(), request.GetInstanceId()); err != nil {
		return nil, err
	}

	if request.GetInstanceType() != s.instanceType {
		return nil, errors.New("invalid instance type")
	}

	errorMsg := request.GetErrorMessage()
	if errorMsg != "" {
		zap.L().Info("Got error heartbeat", zap.String("message", errorMsg))
	} else {
		zap.L().Info("Got success heartbeat")
	}

	return &pb.HeartbeatResponse{
		TaskId:          s.taskId,
		ExecutionStatus: pb.ExecutionStatus_RUNNING.String(),
	}, nil

}

// ------------------------------------------------------------------------

func (s *MockNvctServer) SendResultMetadata(stream pb.Worker_SendResultMetadataServer) error {
	zap.L().Info("Got send result metadata request")

	for {
		resultMetadata, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if err := s.validateWorkerInfo(
			resultMetadata.GetTaskId(),
			resultMetadata.GetInstanceId(),
		); err != nil {
			return err
		}

		if resultMetadata.GetInstanceType() != s.instanceType {
			return errors.New("invalid instance type")
		}

		zap.L().Info(
			"Received task result",
			zap.String("status", resultMetadata.GetStatus().String()),
			zap.Uint32("progress", resultMetadata.GetPercentComplete()),
			zap.String("result name", resultMetadata.GetResultName()),
			zap.Any("metadata", resultMetadata.GetMetadata()),
			zap.Any("error", resultMetadata.GetErrorDetails()),
		)
	}

	resp := &pb.ResultMetadataResponse{}
	if err := stream.SendAndClose(resp); err != nil {
		return err
	}
	return nil

}

// ------------------------------------------------------------------------

func (s *MockNvctServer) GetArtifacts(ctx context.Context, request *pb.ArtifactsRequest) (*pb.ArtifactsResponse, error) {
	zap.L().Info("Got get artifacts request")

	taskId := request.GetTaskId()
	if taskId != s.taskId {
		return nil, errors.New("invalid task id")
	}

	var files []*pb.ArtifactsResponse_ArtifactResponse_ArtifactFile
	for _, artifactFile := range simpleInt8Model.Files {
		url, _ := url.JoinPath(s.artifactServerUrl, "files", artifactFile.Path)
		files = append(files, &pb.ArtifactsResponse_ArtifactResponse_ArtifactFile{
			Path: artifactFile.Path,
			Url:  url,
		})
	}

	artifact := pb.ArtifactsResponse_ArtifactResponse{
		Name:    simpleInt8Model.Name,
		Version: simpleInt8Model.Version,
		Kind:    pb.ArtifactsResponse_ArtifactResponse_MODEL,
		Files:   files,
	}

	resp := &pb.ArtifactsResponse{
		Artifacts: []*pb.ArtifactsResponse_ArtifactResponse{
			&artifact,
		},
	}

	return resp, nil
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) RequestSecretCredentials(ctx context.Context, req *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
	return &pb.SecretCredentialsResponse{
		SecretCredentialsToken: req.TaskId + time.Now().String(),
		Expiration:             timestamppb.New(time.Now().Add(1 * time.Second)),
	}, nil
}

// ------------------------------------------------------------------------

func (s *MockNvctServer) validateWorkerInfo(taskId, instanceId string) error {
	if taskId != s.taskId || instanceId != s.instanceId {
		return errors.New("invalid worker info")
	}

	return nil
}
