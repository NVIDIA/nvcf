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

package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeNatsState struct {
	connected bool
	closed    bool
}

func (s fakeNatsState) Connected() bool { return s.connected }
func (s fakeNatsState) Closed() bool    { return s.closed }

// /livez must stay up while NATS is connected or reconnecting, and fail only
// once the connection is closed and a restart is the only recovery. That the
// state is read without the connection's lock is tested with ConnState in the
// invocation package.
func TestLivenessFailsOnlyWhenNatsIsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state fakeNatsState
		want  int
	}{
		{name: "connected", state: fakeNatsState{connected: true}, want: http.StatusOK},
		{name: "reconnecting", state: fakeNatsState{}, want: http.StatusOK},
		{name: "closed", state: fakeNatsState{closed: true}, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			livenessHandler(tc.state).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
			assert.Equal(t, tc.want, recorder.Code)
		})
	}
}
