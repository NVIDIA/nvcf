/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package runtime

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

// Config holds the parameters needed to construct a Runtime.
type Config struct {
	// SocketPath overrides the socket to connect to. Empty triggers autodetect.
	SocketPath string

	// Namespace is the containerd namespace (ignored for CRI-O).
	Namespace string

	// Logger is required.
	Logger *logrus.Logger
}

// New returns a Runtime implementation for the detected runtime. The caller
// owns the returned Runtime and must Close() it on shutdown.
//
// Implementations are registered by their package init functions via
// RegisterFactory to avoid import cycles (internal/containerd imports this
// package, so this package cannot import internal/containerd).
func New(cfg Config) (Runtime, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("runtime.Config.Logger is required")
	}

	socketPath, rtType, err := Detect(cfg.SocketPath)
	if err != nil {
		return nil, err
	}

	cfg.Logger.WithFields(logrus.Fields{
		"type":   rtType,
		"socket": socketPath,
	}).Info("detected container runtime")

	factory, ok := factories[rtType]
	if !ok {
		return nil, fmt.Errorf("no factory registered for runtime %q (was the package imported?)", rtType)
	}
	return factory(socketPath, cfg)
}

// Factory constructs a Runtime for a specific type.
type Factory func(socketPath string, cfg Config) (Runtime, error)

var factories = map[Type]Factory{}

// RegisterFactory is called from the init() of each runtime backend package.
// Avoids an import cycle (factory needs to reference concrete types, but
// concrete packages need to reference runtime.Runtime).
func RegisterFactory(t Type, f Factory) {
	factories[t] = f
}
