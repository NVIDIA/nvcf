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

package nvcaconfig

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

const (
	envPrefix = "nvca"
)

func Init(configFile string) (Config, error) {
	cfgBytes, err := os.ReadFile(configFile)
	if err != nil {
		return Config{}, err
	}
	return DecodeConfig(cfgBytes)
}

func NewViperDecoderConfig() viper.DecoderConfigOption {
	return func(dc *mapstructure.DecoderConfig) {
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		)
	}
}

func DecodeConfig(data []byte, extraConfigDatas ...[]byte) (cfg Config, err error) {
	v := viper.NewWithOptions(viper.KeyDelimiter("!"))
	v.SetEnvPrefix(envPrefix)
	v.SetConfigType("yaml")
	if err := AutobindAll(v); err != nil {
		return cfg, err
	}
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	for _, extraCfgData := range extraConfigDatas {
		if err := v.MergeConfig(bytes.NewReader(extraCfgData)); err != nil {
			return cfg, fmt.Errorf("merge extra config: %w", err)
		}
	}
	if err := v.Unmarshal(&cfg, NewViperDecoderConfig()); err != nil {
		return cfg, fmt.Errorf("decode merged config: %w", err)
	}
	return cfg, nil
}

func EncodeConfig(cfg Config, extraConfigs ...Config) ([]byte, error) {
	v := viper.NewWithOptions(viper.KeyDelimiter("!"))
	v.SetEnvPrefix(envPrefix)
	v.SetConfigType("yaml")

	yb, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	if err := v.ReadConfig(bytes.NewReader(yb)); err != nil {
		return nil, err
	}

	for _, extraCfg := range extraConfigs {
		yb, err := yaml.Marshal(extraCfg)
		if err != nil {
			return nil, err
		}
		if err := v.MergeConfig(bytes.NewReader(yb)); err != nil {
			return nil, fmt.Errorf("merge extra config: %w", err)
		}
	}

	// Recursively update each field with its camel case alias.
	settings := v.AllSettings()
	aliases := map[string]string{}
	if err := recursiveAutobind(&cfg, "", "", "", "", func(fieldName, _, fieldNameCamelCase string) error {
		aliases[fieldName] = fieldNameCamelCase
		return nil
	}); err != nil {
		return nil, err
	}

	newSettings := map[string]any{}
	for fieldName, fieldNameCamelCase := range aliases {
		fieldSplit := strings.Split(fieldName, ".")
		fieldCamelCaseSplit := strings.Split(fieldNameCamelCase, ".")
		nextOldSettings := settings
		lastNewSettings := newSettings
		for i, elem := range fieldSplit {
			currOldSettings := nextOldSettings[elem]
			if currOldSettings == nil {
				break
			}
			v, ok := currOldSettings.(map[string]any)
			if !ok || i == len(fieldSplit)-1 {
				lastNewSettings[fieldCamelCaseSplit[i]] = currOldSettings
				break
			}
			nextOldSettings = v
			m, ok := lastNewSettings[fieldCamelCaseSplit[i]].(map[string]any)
			if !ok {
				m = map[string]any{}
				lastNewSettings[fieldCamelCaseSplit[i]] = m
			}
			lastNewSettings = m
		}
	}

	buf := &bytes.Buffer{}
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	enc.CompactSeqIndent()
	if err := enc.Encode(newSettings); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
