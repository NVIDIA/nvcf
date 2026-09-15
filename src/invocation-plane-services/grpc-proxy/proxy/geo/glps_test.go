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
package geo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockClientGetter struct {
	mock.Mock
}

func (m *mockClientGetter) Client(ctx context.Context) httpDoer {
	args := m.Called(ctx)
	return args.Get(0).(httpDoer)
}

type mockHTTPDoer struct {
	mock.Mock
}

func (m *mockHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	log.Printf("mockHTTPDoer.Do called with: %v", req)
	args := m.Called(req)
	return args.Get(0).(*http.Response), args.Error(1)
}

func newHTTPResponse(statusCode int, body string, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
	for k, v := range headers {
		resp.Header.Set(k, v)
	}
	return resp
}

func TestGetGeoDataFromIp(t *testing.T) {
	tests := []struct {
		description    string
		ipAddr         net.IP
		expectedResult ipGeoData
		expectedError  string
		httpResp       *http.Response
		httpErr        error
	}{
		{
			description: "Successful Response",
			ipAddr:      net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{
				CountryName: "United States",
				RegionName:  "California",
				CityName:    "Mountain View",
				ISPName:     "Nvidia Corporation",
			},
			expectedError: "",
			httpResp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(strings.NewReader(`{
            "geoLocation": {
                "countryName": "United States",
                "subdivision1Name": "California",
                "cityName": "Mountain View",
                "isp": "Nvidia Corporation"
            }
        }`)),
			},
			httpErr: nil,
		},
		{
			description:    "HTTP Request Error",
			ipAddr:         net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{},
			expectedError:  "network error",
			httpResp:       nil,
			httpErr:        errors.New("network error"),
		},
		{
			description:    "Invalid JSON Body",
			ipAddr:         net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{},
			expectedError:  "expected colon after object key",
			httpResp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				// Trailing comma causes invalid JSON
				Body: io.NopCloser(strings.NewReader(`{
            "geoLocation": {
                "countryName": "United States",
                "subdivision1Name": "California",
                "cityName": "Mountain View",
                "isp": "Google LLC" , "some trailing parts that shouldn't be here"
            }
        }`)),
			},
			httpErr: nil,
		},
		{
			description:    "Invalid FQDN URL",
			ipAddr:         net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{},
			expectedError:  "invalid fqdn",
			httpResp:       nil,
			httpErr:        errors.New("invalid fqdn"),
		},
		{
			description:    "Non-200 Response",
			ipAddr:         net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{},
			expectedError:  "non-200 status code",
			httpResp: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(strings.NewReader(``)),
			},
			httpErr: nil,
		},
		{
			description:    "Unexpected Content Type",
			ipAddr:         net.ParseIP("8.8.8.8"),
			expectedResult: ipGeoData{},
			expectedError:  "the header content-type is not JSON",
			httpResp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/plain"},
				},
				Body: io.NopCloser(strings.NewReader(`{
                    "countryName": "United States",
                    "cityName": "Mountain View"
                }`)),
			},
			httpErr: nil,
		},
	}

	for _, tc := range tests {
		tc := tc // capture range variable
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel() // Optional: run tests in parallel

			doer := new(mockHTTPDoer)
			cg := new(mockClientGetter)

			// Configure mockClientGetter to return mockHTTPDoer
			cg.On("Client", mock.Anything).Return(doer).Once()

			// Configure mockHTTPDoer to return the response or error defined in the test case
			if tc.httpResp != nil || tc.httpErr != nil {
				doer.On("Do", mock.AnythingOfType("*http.Request")).Return(tc.httpResp, tc.httpErr).Once()
			}

			// Initialize mockGlpsClient with mockClientGetter
			g := &glpsClient{
				client: cg,
			}

			// Call the function
			result, err := g.getGeoDataFromIp(context.Background(), tc.ipAddr)

			// Assert expectations
			if tc.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}

			// Assert that all mock expectations were met
			cg.AssertExpectations(t)
			doer.AssertExpectations(t)
		})
	}
}
