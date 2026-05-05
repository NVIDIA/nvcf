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
	"time"
)

const (
	// DefaultReportingInterval is the default period to emit metrics.
	DefaultReportingInterval time.Duration = 5 * time.Second
	// DefaultPrometheusIP is the default IP for HTTP service to bind on.
	DefaultPrometheusIP string = "0.0.0.0"
	// DefaultPrometheusPort is the default port for HTTP service to bind on.
	DefaultPrometheusPort uint = 9103
)

// TelemetryConfig is the configuration for telemetry.
type TelemetryConfig struct {
	Stdout     *StdoutConfig     `mapstructure:"stdout"`
	Prometheus *PrometheusConfig `mapstructure:"prometheus"`
}

// StdoutConfig is the configuration for emitting metrics to stdout.
type StdoutConfig struct {
	ReportingInterval *time.Duration `mapstructure:"reporting_interval"`
}

// PrometheusConfig is the configuration for emitting metrics to Prometheus.
type PrometheusConfig struct {
	IP          *string `mapstructure:"ip"`
	Port        *uint   `mapstructure:"port"`
	TLSDisable  *bool   `mapstructure:"tls_disable"`
	TLSKeyPath  *string `mapstructure:"tls_key_path"`
	TLSCertPath *string `mapstructure:"tls_cert_path"`
}

// DefaultTelemetryConfig returns a configuration that is populated with the
// default values.
func DefaultTelemetryConfig() *TelemetryConfig {
	return &TelemetryConfig{}
}

// Copy returns a deep copy of this configuration.
func (c *TelemetryConfig) Copy() *TelemetryConfig {
	if c == nil {
		return nil
	}

	var o TelemetryConfig
	if c.Stdout != nil {
		o.Stdout = c.Stdout.Copy()
	}

	if c.Prometheus != nil {
		o.Prometheus = c.Prometheus.Copy()
	}

	return &o
}

// Merge combines all values in this configuration with the values in the other
// configuration, with values in the other configuration taking precedence.
// Maps and slices are merged, most other values are overwritten. Complex
// structs define their own merge functionality
func (c *TelemetryConfig) Merge(o *TelemetryConfig) *TelemetryConfig {
	if c == nil {
		if o == nil {
			return nil
		}
		return o.Copy()
	}

	if o == nil {
		return c.Copy()
	}

	r := c.Copy()

	if o.Stdout != nil {
		r.Stdout = o.Stdout.Copy()
	}

	if o.Prometheus != nil {
		r.Prometheus = o.Prometheus.Copy()
	}

	return r
}

// Finalize ensures there no nil pointers.
func (c *TelemetryConfig) Finalize() {
	if c == nil {
		return
	}

	c.Stdout.Finalize()
	c.Prometheus.Finalize()
}

// GoString defines the printable version of this struct.
func (c *TelemetryConfig) GoString() string {
	if c == nil {
		return "(*TelemetryConfig)(nil)"
	}

	return fmt.Sprintf("&TelemetryConfig{"+
		"Stdout:%s, "+
		"Prometheus:%s, "+
		"}",
		c.Stdout.GoString(),
		c.Prometheus.GoString(),
	)
}

// DefaultStdoutConfig returns a configuration that is populated with the
// default values.
func DefaultStdoutConfig() *StdoutConfig {
	return &StdoutConfig{
		ReportingInterval: TimeDuration(DefaultReportingInterval),
	}
}

// Copy returns a deep copy of this configuration.
func (c *StdoutConfig) Copy() *StdoutConfig {
	if c == nil {
		return nil
	}

	return &StdoutConfig{
		ReportingInterval: TimeDurationCopy(c.ReportingInterval),
	}
}

// Merge combines all values in this configuration with the values in the other
// configuration, with values in the other configuration taking precedence.
// Maps and slices are merged, most other values are overwritten. Complex
// structs define their own merge functionality.
func (c *StdoutConfig) Merge(o *StdoutConfig) *StdoutConfig {
	if c == nil {
		if o == nil {
			return nil
		}
		return o.Copy()
	}

	if o == nil {
		return c.Copy()
	}

	r := c.Copy()

	if o.ReportingInterval != nil {
		r.ReportingInterval = TimeDurationCopy(o.ReportingInterval)
	}

	return r
}

// Finalize ensures there no nil pointers.
func (c *StdoutConfig) Finalize() {
	if c == nil {
		return
	}

	d := DefaultStdoutConfig()

	if c.ReportingInterval == nil {
		c.ReportingInterval = d.ReportingInterval
	}
}

// GoString defines the printable version of this struct.
func (c *StdoutConfig) GoString() string {
	if c == nil {
		return "(*StdoutConfig)(nil)"
	}

	return fmt.Sprintf("&StdoutConfig{"+
		"ReportingInterval:%s, "+
		"}",
		TimeDurationGoString(c.ReportingInterval),
	)
}

// DefaultPrometheusConfig returns a configuration that is populated with the
// default values.
func DefaultPrometheusConfig() *PrometheusConfig {
	return &PrometheusConfig{
		IP:          String(DefaultPrometheusIP),
		Port:        Uint(DefaultPrometheusPort),
		TLSDisable:  Bool(false),
		TLSKeyPath:  String(""),
		TLSCertPath: String(""),
	}
}

// Copy returns a deep copy of this configuration.
func (c *PrometheusConfig) Copy() *PrometheusConfig {
	if c == nil {
		return nil
	}

	return &PrometheusConfig{
		IP:          StringCopy(c.IP),
		Port:        UintCopy(c.Port),
		TLSDisable:  BoolCopy(c.TLSDisable),
		TLSKeyPath:  StringCopy(c.TLSKeyPath),
		TLSCertPath: StringCopy(c.TLSCertPath),
	}
}

// Merge combines all values in this configuration with the values in the other
// configuration, with values in the other configuration taking precedence.
// Maps and slices are merged, most other values are overwritten. Complex
// structs define their own merge functionality.
func (c *PrometheusConfig) Merge(o *PrometheusConfig) *PrometheusConfig {
	if c == nil {
		if o == nil {
			return nil
		}
		return o.Copy()
	}

	if o == nil {
		return c.Copy()
	}

	r := c.Copy()

	if o.IP != nil {
		r.IP = StringCopy(o.IP)
	}

	if o.Port != nil {
		r.Port = UintCopy(o.Port)
	}

	if o.TLSDisable != nil {
		r.TLSDisable = BoolCopy(o.TLSDisable)
	}

	if o.TLSKeyPath != nil {
		r.TLSKeyPath = StringCopy(o.TLSKeyPath)
	}

	if o.TLSCertPath != nil {
		r.TLSCertPath = StringCopy(o.TLSCertPath)
	}

	return r
}

// Finalize ensures there no nil pointers.
func (c *PrometheusConfig) Finalize() {
	if c == nil {
		return
	}

	d := DefaultPrometheusConfig()

	if c.IP == nil {
		c.IP = d.IP
	}

	if c.Port == nil {
		c.Port = d.Port
	}

	if c.TLSDisable == nil {
		c.TLSDisable = d.TLSDisable
	}

	if c.TLSKeyPath == nil {
		c.TLSKeyPath = d.TLSKeyPath
	}

	if c.TLSCertPath == nil {
		c.TLSCertPath = d.TLSCertPath
	}
}

// GoString defines the printable version of this struct.
func (c *PrometheusConfig) GoString() string {
	if c == nil {
		return "(*PrometheusConfig)(nil)"
	}

	return fmt.Sprintf("&PrometheusConfig{"+
		"IP:%s, "+
		"Port:%s, "+
		"TLSDisable: %s, "+
		"TLSKeyPath:%s, "+
		"TLSCertPath:%s, "+
		"}",
		StringGoString(c.IP),
		UintGoString(c.Port),
		BoolGoString(c.TLSDisable),
		StringGoString(c.TLSKeyPath),
		StringGoString(c.TLSCertPath),
	)
}
