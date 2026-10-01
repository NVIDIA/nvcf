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

package registration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
)

// pylonText is a trimmed Pylon 0.15 exposition: the three series the
// observer reads among families it ignores.
const pylonText = `# HELP pylon_build_info Build information.
# TYPE pylon_build_info gauge
pylon_build_info{version="0.15.2"} 1
# HELP pylon_registration_stream_connected Registration stream state per router.
# TYPE pylon_registration_stream_connected gauge
pylon_registration_stream_connected{router="router-0"} 1
pylon_registration_stream_connected{router="router-1"} 0
pylon_registration_stream_connected{router="router-2"} 1
# HELP pylon_reverse_tunnel_connected Reverse tunnel state per router.
# TYPE pylon_reverse_tunnel_connected gauge
pylon_reverse_tunnel_connected{router="router-0"} 1
pylon_reverse_tunnel_connected{router="router-2"} 0
# HELP pylon_registration_stream_closures_total Registration stream closures.
# TYPE pylon_registration_stream_closures_total counter
pylon_registration_stream_closures_total{router="router-1",reason="unauthenticated"} 4
pylon_registration_stream_closures_total{router="router-1",reason="invalid_argument"} 1
pylon_registration_stream_closures_total{router="router-1",reason="idle_timeout"} 9
pylon_registration_stream_closures_total{router="router-0",reason="io"} 2
# HELP pylon_request_duration_seconds Request latency.
# TYPE pylon_request_duration_seconds histogram
pylon_request_duration_seconds_bucket{le="0.1"} 3
pylon_request_duration_seconds_bucket{le="+Inf"} 5
pylon_request_duration_seconds_sum 1.5
pylon_request_duration_seconds_count 5
`

func TestParse(t *testing.T) {
	s, err := Parse(strings.NewReader(pylonText))
	require.NoError(t, err)
	assert.Equal(t, []string{"router-0", "router-2"}, s.Streams)
	assert.Equal(t, []string{"router-0"}, s.Tunnels)
	assert.Equal(t, map[Closure]float64{
		{Router: "router-1", Reason: ClosureUnauthenticated}: 4,
		{Router: "router-1", Reason: ClosureInvalidArgument}: 1,
	}, s.Rejections, "only rejecting reasons")
}

func TestParseVariants(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Sample
	}{
		{
			name: "empty exposition",
			text: "",
			want: Sample{Rejections: map[Closure]float64{}},
		},
		{
			name: "untyped samples without metadata and a missing router label",
			text: "pylon_registration_stream_connected 1\npylon_reverse_tunnel_connected{router=\"r\"} 1\n",
			want: Sample{Streams: []string{""}, Tunnels: []string{"r"}, Rejections: map[Closure]float64{}},
		},
		{
			name: "OpenMetrics counter family without the _total suffix",
			text: `# TYPE pylon_registration_stream_closures counter
pylon_registration_stream_closures_total{router="r",reason="unauthenticated"} 2
pylon_registration_stream_closures_created{router="r",reason="unauthenticated"} 1.7e+09
# EOF
`,
			want: Sample{Rejections: map[Closure]float64{{Router: "r", Reason: ClosureUnauthenticated}: 2}},
		},
		{
			name: "a router listed twice counts once",
			text: "pylon_registration_stream_connected{router=\"r\",stream=\"a\"} 1\npylon_registration_stream_connected{router=\"r\",stream=\"b\"} 1\n",
			want: Sample{Streams: []string{"r"}, Rejections: map[Closure]float64{}},
		},
		{
			name: "closures of one router and reason add up",
			text: "pylon_registration_stream_closures_total{router=\"r\",reason=\"invalid_argument\",code=\"a\"} 1\npylon_registration_stream_closures_total{router=\"r\",reason=\"invalid_argument\",code=\"b\"} 2\n",
			want: Sample{Rejections: map[Closure]float64{{Router: "r", Reason: ClosureInvalidArgument}: 3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(strings.NewReader(tt.text))
			require.NoError(t, err)
			assert.Equal(t, tt.want, s)
		})
	}
}

func TestParseRejectsMalformedText(t *testing.T) {
	for _, text := range []string{
		"pylon_registration_stream_connected{router=\"r\" 1\n",
		"pylon_registration_stream_connected{router=\"r\"} one\n",
		"<html>not metrics</html>\n",
	} {
		_, err := Parse(strings.NewReader(text))
		assert.Error(t, err, text)
	}
}

func TestPodMetricsURL(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{PodIP: "10.1.2.3"}}
	assert.Equal(t, "http://10.1.2.3:9089/metrics", PodMetricsURL(pod))
	pod.Status.PodIP = "fd00::7"
	assert.Equal(t, "http://[fd00::7]:9089/metrics", PodMetricsURL(pod))
}

func TestScrapeFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/plain; version=0.0.4", r.Header.Get("Accept"))
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(pylonText))
		case "/status":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/parse":
			_, _ = w.Write([]byte("{\"json\": true}"))
		case "/large":
			line := strings.Repeat("#", 1023) + "\n"
			for i := 0; i <= maxBodyBytes/len(line); i++ {
				_, _ = w.Write([]byte(line))
			}
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	t.Cleanup(srv.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	o := &Observer{Client: srv.Client(), Timeout: 200 * time.Millisecond}
	s, err := o.scrape(context.Background(), srv.URL+"/ok")
	require.NoError(t, err)
	assert.Equal(t, []string{"router-0", "router-2"}, s.Streams)

	tests := []struct {
		url  string
		want metrics.ScrapeFailureReason
	}{
		{url: srv.URL + "/status", want: metrics.ScrapeFailureStatus},
		{url: srv.URL + "/parse", want: metrics.ScrapeFailureParse},
		{url: srv.URL + "/large", want: metrics.ScrapeFailureParse},
		{url: srv.URL + "/slow", want: metrics.ScrapeFailureTimeout},
		{url: closed.URL + "/metrics", want: metrics.ScrapeFailureConnect},
		{url: "http://bad host/metrics", want: metrics.ScrapeFailureConnect},
	}
	for _, tt := range tests {
		t.Run(string(tt.want)+" "+tt.url, func(t *testing.T) {
			_, err := o.scrape(context.Background(), tt.url)
			var se *ScrapeError
			require.True(t, errors.As(err, &se), "%v", err)
			assert.Equal(t, tt.want, se.Reason)
			assert.True(t, strings.HasPrefix(err.Error(), string(tt.want)+": "), err.Error())
			assert.NotNil(t, errors.Unwrap(err))
		})
	}
}

func TestTransportFailureClassification(t *testing.T) {
	assert.Equal(t, metrics.ScrapeFailureTimeout, transportFailure(fmt.Errorf("read: %w", context.DeadlineExceeded)).Reason)
	assert.Equal(t, metrics.ScrapeFailureTimeout, transportFailure(timeoutError{}).Reason)
	assert.Equal(t, metrics.ScrapeFailureConnect, transportFailure(errors.New("connection reset by peer")).Reason)
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestDefaultTimeout(t *testing.T) {
	assert.Equal(t, DefaultTimeout, (&Observer{}).timeout())
	assert.Equal(t, time.Second, (&Observer{Timeout: time.Second}).timeout())
}
