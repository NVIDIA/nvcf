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

package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
)

func TestNewRootCommandShape(t *testing.T) {
	cmd := NewRootCommand(context.Background())

	if cmd.Use != "init" {
		t.Errorf("expected Use %q, got %q", "init", cmd.Use)
	}
	if !cmd.SilenceUsage {
		t.Error("expected SilenceUsage to be true")
	}
}

func TestNewRootCommandConfigFlag(t *testing.T) {
	cmd := NewRootCommand(context.Background())

	if cmd.PersistentFlags().Lookup("config") == nil {
		t.Error("expected --config persistent flag to be registered")
	}
}

func TestNewRootCommandInitConfigFields(t *testing.T) {
	cmd := NewRootCommand(context.Background())

	configType := reflect.TypeOf(configs.InitConfig{})
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		if field.Type.Kind() == reflect.Struct {
			embedType := field.Type
			for j := 0; j < embedType.NumField(); j++ {
				envName := embedType.Field(j).Tag.Get("mapstructure")
				if envName == "" {
					continue
				}
				if cmd.Flags().Lookup(envName) == nil {
					t.Errorf("expected flag %q for field %s.%s to be registered",
						envName, field.Name, embedType.Field(j).Name)
				}
			}
			continue
		}
		envName := field.Tag.Get("mapstructure")
		if envName == "" {
			continue
		}
		if cmd.Flags().Lookup(envName) == nil {
			t.Errorf("expected flag %q for field %s to be registered", envName, field.Name)
		}
	}
}
