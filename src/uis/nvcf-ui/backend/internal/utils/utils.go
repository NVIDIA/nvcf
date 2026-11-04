// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const logLevel = "LOG_LEVEL"

// GetEnvOr returns the value of the environment variable named by key, or
// fallback if the variable is unset or empty.
func GetEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func ConfigLogger() zerolog.Logger {
	logger := zerolog.New(os.Stdout).With().Caller().Timestamp().Logger()
	// To disable logging entirely, pass [zerolog.Disabled]
	logLevel, err := zerolog.ParseLevel(os.Getenv(logLevel))
	if err != nil {
		logger.Fatal().Err(err).Msgf("Invalid %s", logLevel)
	}
	if logLevel.String() == "" {
		logLevel = zerolog.InfoLevel
	}

	return logger.Level(logLevel)
}

func InitK8sClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register k8s scheme: %w", err)
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("get k8s config: %w", err)
	}
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create k8s client: %w", err)
	}
	return k8sClient, nil
}
