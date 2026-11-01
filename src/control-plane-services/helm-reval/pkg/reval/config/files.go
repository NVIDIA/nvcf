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

package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func ParseConfigFiles(cfgFiles []string, v *viper.Viper, logger *zap.Logger) error {
	nonEmptyCfgFiles := make([]string, 0, len(cfgFiles))
	for _, cfgFile := range cfgFiles {
		if trimmed := strings.TrimSpace(cfgFile); trimmed != "" {
			nonEmptyCfgFiles = append(nonEmptyCfgFiles, trimmed)
		}
	}

	cfgFiles = nonEmptyCfgFiles
	if len(cfgFiles) == 0 {
		logger.Warn("No config files provided, using default config values")
	}

	for _, cfgFile := range cfgFiles {
		v.SetConfigFile(cfgFile)
		logger.Info("Merging config file", zap.String("config_file", cfgFile))
		if err := v.MergeInConfig(); err != nil {
			return fmt.Errorf("failed to merge config file '%s': %w", cfgFile, err)
		}
	}

	return nil
}
