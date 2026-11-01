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

package version

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoundTrip(t *testing.T) {
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}
	Version = "1.0.0"
	GitHash = "abcd1234"
	Dirty = ""
	ReleaseTag = "v1.0.0"
	t.Cleanup(func() {
		Version = ""
		GitHash = ""
		Dirty = ""
		ReleaseTag = ""
	})
	httpClient.Transport = NewTransport(httpClient.Transport, "testApp")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "testApp/1.0.0", r.Header.Get("User-Agent"))
	}))
	t.Cleanup(s.Close)

	resp, err := httpClient.Get(s.URL + "/versiontest")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
