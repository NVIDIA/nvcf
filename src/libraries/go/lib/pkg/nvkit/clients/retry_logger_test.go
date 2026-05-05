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

package clients

import (
	"bytes"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.uber.org/zap"
)

// memorySink implements zap.Sink by writing all messages to a buffer.
type memorySink struct {
	*bytes.Buffer
}

// Implement Close and Sync as no-ops to satisfy the interface. The Write
// method is provided by the embedded buffer.
func (s *memorySink) Close() error { return nil }
func (s *memorySink) Sync() error  { return nil }

func TestRetryLogger(t *testing.T) {
	sink := &memorySink{new(bytes.Buffer)}
	zap.RegisterSink("memory", func(*url.URL) (zap.Sink, error) {
		return sink, nil
	})

	conf := zap.NewProductionConfig()
	conf.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	conf.OutputPaths = []string{"memory://"}

	l, err := conf.Build()
	require.Nil(t, err)
	zap.ReplaceGlobals(l)

	rL := newRetryLogger()
	// assert an error message is logged
	rL.Error("error message", "errorKey", "errorValue")
	output := sink.String()
	sink.Reset()
	assert.Contains(t, output, "\"msg\":\"error message\",\"errorKey\":\"errorValue\"")

	// assert an info message is logged
	rL.Info("info message", "infoKey", "infoValue")
	output = sink.String()
	sink.Reset()
	assert.Contains(t, output, "\"msg\":\"info message\",\"infoKey\":\"infoValue\"")

	// assert a debug message is logged
	rL.Debug("debug message", "debugKey", "debugValue")
	output = sink.String()
	sink.Reset()
	assert.Contains(t, output, "\"msg\":\"debug message\",\"debugKey\":\"debugValue\"")

	// assert a warning message is logged
	rL.Warn("warn message", "warnKey", "warnValue")
	output = sink.String()
	assert.Contains(t, output, "\"msg\":\"warn message\",\"warnKey\":\"warnValue\"")
}
