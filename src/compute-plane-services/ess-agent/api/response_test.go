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

package api

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// CreateTestResponse creates a mock http.Response with a request for testing purposes
func createTestResponse(statusCode int, body, reqMethod, reqURL string) Response {
	req, err := http.NewRequest(reqMethod, reqURL, nil)
	if err != nil {
		panic(err)
	}

	header := make(http.Header)
	header.Add("X-ESS-NAMESPACE", "test")
	header.Add("X-ESS-Request-Id", "123456")
	return Response{
		Response: &http.Response{
			Status:     http.StatusText(statusCode),
			StatusCode: statusCode,
			Body:       ioutil.NopCloser(bytes.NewBufferString(body)),
			Header:     header,
			Request:    req,
		},
	}
}

func TestResponseError_Error(t *testing.T) {
	tests := map[string]struct {
		reqMethod        string
		respBody         []byte
		reqURL           string
		respStatusCode   int
		expcetedErrorStr string
	}{
		"ess: detail error is present in the error body": {
			reqMethod: "GET",
			respBody: []byte(`{
				"type": "about:blank",
				"title": "forbidden",
				"detail": "Required header 'X-ABC' is not present.",
				"instance": "/v1/testpath",
				"properties": null
			}`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusForbidden,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: GET http://ess.agent.com/v1/testpath | Code: 403 | Request Id: 123456 | Title: forbidden | Detail: Required header 'X-ABC' is not present.",
		},
		"ess: detail error is not present in the error body": {
			reqMethod: "GET",
			respBody: []byte(`{
				"type": "about:blank",
				"title": "forbidden",
				"instance": "/v1/testpath",
				"properties": null
			}`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusForbidden,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: GET http://ess.agent.com/v1/testpath | Code: 403 | Request Id: 123456 | Title: forbidden | Detail: undetermined",
		},
		"ess: title info is not present in the error body": {
			reqMethod: "POST",
			respBody: []byte(`{
				"type": "about:blank",
				"detail": "related to 501",
				"instance": "/v1/testpath",
				"properties": null
			}`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusNotImplemented,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: POST http://ess.agent.com/v1/testpath | Code: 501 | Request Id: 123456 | Title: undetermined | Detail: related to 501",
		},
		"ess: title and detail are missing": {
			reqMethod: "POST",
			respBody: []byte(`{
				"type": "about:blank",
				"instance": "/v1/testpath",
				"properties": null
			}`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusNotImplemented,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: POST http://ess.agent.com/v1/testpath | Code: 501 | Request Id: 123456 | Title: undetermined | Detail: undetermined",
		},
		"ess: error is not the spec": {
			reqMethod:        "POST",
			respBody:         []byte(`some weird error we didn't account for`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusNotImplemented,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: POST http://ess.agent.com/v1/testpath | Code: 501 | Request Id: 123456 | Title: undetermined | Detail: some weird error we didn't account for",
		},
		"ess: error is not the spec but is vault like": {
			reqMethod: "POST",
			respBody: []byte(`{
				"errors": [
					"a error",
					"b error"
				]
			}`),
			reqURL:           "http://ess.agent.com/v1/testpath",
			respStatusCode:   http.StatusNotImplemented,
			expcetedErrorStr: "Error making API request | Namespace: test | URL: POST http://ess.agent.com/v1/testpath | Code: 501 | Request Id: 123456 | Title: undetermined | Detail: a error,b error",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			resp := createTestResponse(tt.respStatusCode, string(tt.respBody), tt.reqMethod, tt.reqURL)
			respErr := resp.Error()
			require.Error(t, respErr)
			require.Equal(t, respErr.Error(), tt.expcetedErrorStr)
		})
	}
}
