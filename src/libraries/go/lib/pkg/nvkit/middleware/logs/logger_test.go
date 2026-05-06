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

package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/assert"
)

const (
	testEndpointLog = "test endpoint!\n"
)

var (
	buf        = new(bytes.Buffer)
	testWriter = bufio.NewWriter(buf)
	testError  = errors.New("test-error")
)

func TestBaseEndpointLogger(t *testing.T) {
	// Set up logger for testing with bytes buffered writer for easy testing
	logger := log.NewLogfmtLogger(testWriter)

	// Add logger to endpoint
	ep := endpoint.Endpoint(testEndpoint)
	ep = BaseEndpointLogger(logger)(testEndpoint)
	// Execute endpoint and check that the logger middleware added the logs
	_, err := ep(context.Background(), struct{}{})
	assert.Nil(t, err)
	testWriter.Flush()
	// Check that the log is added after the logs from endpoint
	assert.Contains(t, buf.String(), fmt.Sprintf("%stransport_error=null took=", testEndpointLog))

	// Add logger to test endpoint that returns error
	buf.Reset() // Clear the buffer for the next test
	epWithErr := endpoint.Endpoint(testEndpointWithError)
	epWithErr = BaseEndpointLogger(logger)(testEndpointWithError)
	// Execute endpoint and check that the logger middleware added the logs
	_, err = epWithErr(context.Background(), struct{}{})
	assert.Equal(t, testError, err)
	testWriter.Flush()
	// Check that the log is added after the logs from endpoint and includes the error from endpoint
	assert.Contains(t, buf.String(), fmt.Sprintf("%stransport_error=%s took=", testEndpointLog, testError.Error()))
}

func testEndpoint(context.Context, interface{}) (interface{}, error) {
	testWriter.Write([]byte(testEndpointLog))
	return struct{}{}, nil
}

func testEndpointWithError(context.Context, interface{}) (interface{}, error) {
	testWriter.Write([]byte(testEndpointLog))
	return struct{}{}, testError
}
