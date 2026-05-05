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

package config

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestTelemetryConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    *TelemetryConfig
	}{
		{
			"nil",
			nil,
		},
		{
			"empty",
			&TelemetryConfig{},
		},
		{
			"stdout",
			&TelemetryConfig{
				Stdout: &StdoutConfig{},
			},
		},
		{
			"prometheus",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port: Uint(8888),
				},
			},
		},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s", i, tc.name), func(t *testing.T) {
			r := tc.a.Copy()
			if !reflect.DeepEqual(tc.a, r) {
				t.Errorf("\nexp: %#v\nact: %#v", tc.a, r)
			}
		})
	}
}

func TestTelemetryConfig_Merge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    *TelemetryConfig
		b    *TelemetryConfig
		r    *TelemetryConfig
	}{
		{
			"nil_a",
			nil,
			&TelemetryConfig{},
			&TelemetryConfig{},
		},
		{
			"nil_b",
			&TelemetryConfig{},
			nil,
			&TelemetryConfig{},
		},
		{
			"nil_both",
			nil,
			nil,
			nil,
		},
		{
			"empty",
			&TelemetryConfig{},
			&TelemetryConfig{},
			&TelemetryConfig{},
		},
		{
			"stdout_overrides",
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
			&TelemetryConfig{
				Stdout: &StdoutConfig{
					ReportingInterval: TimeDuration(time.Minute),
				},
			},
			&TelemetryConfig{
				Stdout: &StdoutConfig{
					ReportingInterval: TimeDuration(time.Minute),
				},
			},
		},
		{
			"stdout_empty_one",
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
			&TelemetryConfig{},
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
		},
		{
			"stdout_empty_two",
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
			&TelemetryConfig{},
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
		},
		{
			"stdout_same",
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
			&TelemetryConfig{
				Stdout: &StdoutConfig{ReportingInterval: TimeDuration(time.Second)},
			},
		},
		{
			"prometheus_overrides",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{},
			},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port: Uint(8080),
				},
			},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port: Uint(8080),
				},
			},
		},
		{
			"prometheus_empty_one",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					TLSDisable:  Bool(false),
					TLSKeyPath:  String("key"),
					TLSCertPath: String("cert"),
				},
			},
			&TelemetryConfig{},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					TLSDisable:  Bool(false),
					TLSKeyPath:  String("key"),
					TLSCertPath: String("cert"),
				},
			},
		},
		{
			"prometheus_empty_two",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{},
			},
			&TelemetryConfig{},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{},
			},
		},
		{
			"prometheus_same",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port: Uint(8080),
				},
			},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port:       Uint(8080),
					TLSDisable: Bool(true),
				},
			},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port:       Uint(8080),
					TLSDisable: Bool(true),
				},
			},
		},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s", i, tc.name), func(t *testing.T) {
			r := tc.a.Merge(tc.b)
			if !reflect.DeepEqual(tc.r, r) {
				t.Errorf("\nexp: %#v\nact: %#v", tc.r, r)
			}
		})
	}
}

func TestTelemetryConfig_Finalize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		i    *TelemetryConfig
		r    *TelemetryConfig
	}{
		{
			"empty",
			&TelemetryConfig{},
			&TelemetryConfig{},
		},
		{
			"with_stdout",
			&TelemetryConfig{
				Stdout: &StdoutConfig{
					ReportingInterval: TimeDuration(time.Minute * 5),
				},
			},
			&TelemetryConfig{
				Stdout: &StdoutConfig{
					ReportingInterval: TimeDuration(time.Minute * 5),
				},
			},
		},
		{
			"with_prometheus",
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					Port: Uint(80),
				},
			},
			&TelemetryConfig{
				Prometheus: &PrometheusConfig{
					IP:          String("0.0.0.0"),
					Port:        Uint(80),
					TLSDisable:  Bool(false),
					TLSKeyPath:  String(""),
					TLSCertPath: String(""),
				},
			},
		},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s", i, tc.name), func(t *testing.T) {
			tc.i.Finalize()
			if !reflect.DeepEqual(tc.r, tc.i) {
				t.Errorf("\nexp: %#v\nact: %#v", tc.r, tc.i)
			}
		})
	}
}
