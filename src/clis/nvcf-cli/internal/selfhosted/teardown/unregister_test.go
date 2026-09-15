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

package teardown

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClusterDeleter is a test double for ClusterDeleter.
type fakeClusterDeleter struct {
	deleteErr    error
	deleteCalled bool
	ncaID        string
	clusterID    string
}

func (f *fakeClusterDeleter) DeleteCluster(_ context.Context, _, ncaID, clusterID string) error {
	f.deleteCalled = true
	f.ncaID = ncaID
	f.clusterID = clusterID
	return f.deleteErr
}

func TestUnregister_HappyPath(t *testing.T) {
	fake := &fakeClusterDeleter{}
	err := Unregister(context.Background(), fake, "http://sis", "nca-xyz", "cl-abc123")
	require.NoError(t, err)
	assert.True(t, fake.deleteCalled)
	assert.Equal(t, "nca-xyz", fake.ncaID)
	assert.Equal(t, "cl-abc123", fake.clusterID)
}

func TestUnregister_NotFoundIsIdempotent(t *testing.T) {
	// Both "not found" and "404" variants must be swallowed.
	for _, errMsg := range []string{"cluster not found", "HTTP 404 Not Found", "404 page not found"} {
		t.Run(errMsg, func(t *testing.T) {
			fake := &fakeClusterDeleter{deleteErr: errors.New(errMsg)}
			err := Unregister(context.Background(), fake, "http://sis", "nca-xyz", "cl-gone")
			require.NoError(t, err, "expected nil for not-found error: %q", errMsg)
		})
	}
}

func TestUnregister_500Propagates(t *testing.T) {
	serverErr := errors.New("internal server error 500")
	fake := &fakeClusterDeleter{deleteErr: serverErr}
	err := Unregister(context.Background(), fake, "http://sis", "nca-xyz", "cl-abc")
	require.Error(t, err)
	assert.ErrorIs(t, err, serverErr)
}
