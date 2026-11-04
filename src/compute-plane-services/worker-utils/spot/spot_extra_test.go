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

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetState clears the package-global request map so tests do not interfere.
func resetState(t *testing.T) {
	t.Helper()
	instanceRequestsLock.Lock()
	instanceRequests = make(map[uuid.UUID]*instanceRequest)
	instanceRequestsLock.Unlock()
}

// chdirTemp moves into a temp dir so requestSpotInstances writes its .env file
// somewhere disposable rather than polluting the package directory.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

func TestRequestSpotInstances(t *testing.T) {
	resetState(t)
	dir := chdirTemp(t)

	env := base64.StdEncoding.EncodeToString([]byte("FOO=bar\n"))
	result := requestSpotInstances(3, "gpu.large", "nvcr.io/image:latest", env)

	requestID, ok := result["requestId"]
	require.True(t, ok)
	require.NotEqual(t, uuid.Nil, requestID)

	instanceRequestsLock.RLock()
	req, ok := instanceRequests[requestID]
	instanceRequestsLock.RUnlock()
	require.True(t, ok)
	require.Len(t, req.instances, 3)

	for _, inst := range req.instances {
		assert.Equal(t, "ACTIVE", inst.status)
		assert.Equal(t, "gpu.large", inst.instanceType)
		assert.Equal(t, "nvcr.io/image:latest", inst.containerImage)
	}

	// requestSpotInstances writes a .env file per instance into the cwd.
	contents, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(contents), "INSTANCE_ID=")
	assert.Contains(t, string(contents), "FOO=bar")
}

func TestRequestSpotInstancesZeroCount(t *testing.T) {
	resetState(t)
	chdirTemp(t)

	result := requestSpotInstances(0, "gpu.small", "img", "")
	requestID := result["requestId"]

	instanceRequestsLock.RLock()
	req := instanceRequests[requestID]
	instanceRequestsLock.RUnlock()
	require.NotNil(t, req)
	assert.Empty(t, req.instances)
}

func TestDescribeSpotInstanceRequests(t *testing.T) {
	resetState(t)
	chdirTemp(t)

	env := base64.StdEncoding.EncodeToString([]byte("K=V\n"))
	requestID := requestSpotInstances(2, "gpu.xl", "image:tag", env)["requestId"]

	resp := describeSpotInstanceRequests(requestID)
	described, ok := resp.(DescribeSpotInstanceRequests)
	require.True(t, ok)
	require.Len(t, described.SpotInstanceRequests, 2)

	for _, ir := range described.SpotInstanceRequests {
		assert.Equal(t, requestID.String(), ir.SpotInstanceRequestId)
		assert.Equal(t, "gpu.xl", ir.LaunchSpecification.InstanceType)
		assert.Equal(t, "image:tag", ir.LaunchSpecification.ContainerImage)
		assert.Equal(t, "active", ir.State)
		assert.Equal(t, "running", ir.Status.Code)
		require.NotNil(t, ir.InstanceId)
		require.NotNil(t, ir.SpotCloudProvider)
		assert.Equal(t, "GFN", *ir.SpotCloudProvider)
		require.NotNil(t, ir.InstanceState)
		assert.Equal(t, "running", ir.InstanceState.Name)
	}
}

func TestDescribeSpotInstanceRequestsUnknownID(t *testing.T) {
	resetState(t)

	resp := describeSpotInstanceRequests(uuid.New())
	described, ok := resp.(DescribeSpotInstanceRequests)
	require.True(t, ok)
	assert.Empty(t, described.SpotInstanceRequests)
}

func TestTerminateInstances(t *testing.T) {
	resetState(t)
	chdirTemp(t)

	requestID := requestSpotInstances(2, "gpu.xl", "image:tag", "")["requestId"]

	instanceRequestsLock.RLock()
	instIDs := []string{
		instanceRequests[requestID].instances[0].id.String(),
		instanceRequests[requestID].instances[1].id.String(),
	}
	instanceRequestsLock.RUnlock()

	resp := terminateInstances(instIDs)
	_, ok := resp.(map[string]any)
	require.True(t, ok)

	instanceRequestsLock.RLock()
	for _, inst := range instanceRequests[requestID].instances {
		assert.Equal(t, "CANCELED", inst.status)
	}
	instanceRequestsLock.RUnlock()
}

func TestTerminateInstancesEmpty(t *testing.T) {
	resetState(t)
	resp := terminateInstances(nil)
	_, ok := resp.(map[string]any)
	require.True(t, ok)
}

func TestTerminateSpotInstanceRequests(t *testing.T) {
	resetState(t)
	chdirTemp(t)

	requestID := requestSpotInstances(1, "gpu.xl", "image:tag", "")["requestId"]

	instanceRequestsLock.RLock()
	_, present := instanceRequests[requestID]
	instanceRequestsLock.RUnlock()
	require.True(t, present)

	resp := terminateSpotInstanceRequests(requestID)
	_, ok := resp.(map[string]any)
	require.True(t, ok)

	instanceRequestsLock.RLock()
	_, present = instanceRequests[requestID]
	instanceRequestsLock.RUnlock()
	assert.False(t, present)
}
