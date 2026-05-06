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
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/viper"
)

// SupportedTags represents struct field tags recognized by the flag generator.
const (
	tagMapStructure = "mapstructure"
	tagYAML         = "yaml"
)

// AutobindAll binds struct fields to envs and aliases in v.
//
// Tags supported:
// - `mapstructure`: key for unmarshalling and flag name if `flag` is not set
// - `yaml`: serialized field name alias when explicitly set
func AutobindAll(v *viper.Viper) error {
	upperEnvPrefix := strings.ToUpper(v.GetEnvPrefix()) + "_"
	return recursiveAutobind(&Config{}, upperEnvPrefix, "", "", "", func(fieldName, envName, fieldNameCamelCase string) error {
		if err := v.BindEnv(fieldName, envName); err != nil {
			return err
		}
		// Also bind env with alternative delimiter for configs.
		fieldNameWithAltDelim := strings.ReplaceAll(fieldName, ".", "!")
		if err := v.BindEnv(fieldNameWithAltDelim, envName); err != nil {
			return err
		}
		if fieldNameCamelCase != fieldName {
			v.RegisterAlias(fieldNameCamelCase, fieldName)
		}
		return nil
	})
}

// AutobindEnvs binds struct fields to envs in v.
//
// Tags supported:
// - `mapstructure`: key for unmarshalling and flag name if `flag` is not set
// - `yaml`: serialized field name alias when explicitly set
func AutobindEnvs(v *viper.Viper) error {
	upperEnvPrefix := strings.ToUpper(v.GetEnvPrefix()) + "_"
	return recursiveAutobind(&Config{}, upperEnvPrefix, "", "", "", func(fieldName, envName, fieldNameCamelCase string) error {
		if err := v.BindEnv(fieldName, envName); err != nil {
			return err
		}
		// Also bind env with alternative delimiter for configs.
		fieldNameWithAltDelim := strings.ReplaceAll(fieldName, ".", "!")
		return v.BindEnv(fieldNameWithAltDelim, envName)
	})
}

// AutobindAliases binds struct fields to aliases in v.
//
// Tags supported:
// - `mapstructure`: key for unmarshalling and flag name if `flag` is not set
// - `yaml`: serialized field name alias when explicitly set
func AutobindAliases(v *viper.Viper) error {
	upperEnvPrefix := strings.ToUpper(v.GetEnvPrefix()) + "_"
	return recursiveAutobind(&Config{}, upperEnvPrefix, "", "", "", func(fieldName, envName, fieldNameCamelCase string) error {
		if fieldNameCamelCase != fieldName {
			v.RegisterAlias(fieldNameCamelCase, fieldName)
		}
		return nil
	})
}

func recursiveAutobind(
	config any,
	upperEnvPrefix string,
	prefixField, prefixFieldUnchanged, prefixEnv string,
	bind func(fieldName, envName, fieldNameCamelCase string) error,
) error {
	val := reflect.ValueOf(config)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return fmt.Errorf("config must not be nil")
	}

	val = val.Elem()
	typ := val.Type()
	if typ.Kind() != reflect.Struct {
		return fmt.Errorf("config must be type struct")
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldVal := val.Field(i)

		if !field.IsExported() {
			continue
		}

		tagFull := field.Tag.Get(tagMapStructure)
		if tagFull == "" {
			tagFull = field.Name
		}
		if tagFull == "-" {
			continue
		}

		var (
			squash bool
		)
		tag := tagFull
		tagSplit := strings.Split(tagFull, ",")
		if len(tagSplit) > 1 {
			tag = tagSplit[0]
			for _, d := range tagSplit[1:] {
				switch d {
				case "squash":
					squash = true
				default:
					return fmt.Errorf("unsupported tag directive %q", d)
				}
			}
		}

		fieldName := tag
		fieldNameCamelCase := nameToCamelCase(tag)
		if yamlFieldName := explicitTagName(field.Tag.Get(tagYAML)); yamlFieldName != "" {
			fieldNameCamelCase = yamlFieldName
		}
		envName := joinByCamelCase(tag, "_")

		fieldName = joinPrefix(prefixField, fieldName, ".", squash)
		fieldNameCamelCase = joinPrefix(prefixFieldUnchanged, fieldNameCamelCase, ".", squash)
		envName = joinPrefix(prefixEnv, envName, "_", squash)

		fieldName = strings.ToLower(fieldName)
		envName = strings.ToUpper(envName)

		// Recurse into nested structs
		if field.Type.Kind() == reflect.Struct {
			if err := recursiveAutobind(fieldVal.Addr().Interface(), upperEnvPrefix, fieldName, fieldNameCamelCase, envName, bind); err != nil {
				return err
			}
			continue
		}

		if !strings.HasPrefix(envName, upperEnvPrefix) {
			envName = upperEnvPrefix + envName
		}
		if err := bind(fieldName, envName, fieldNameCamelCase); err != nil {
			return err
		}
	}

	return nil
}

func explicitTagName(tagFull string) string {
	if tagFull == "" {
		return ""
	}
	tag := strings.Split(tagFull, ",")[0]
	if tag == "-" {
		return ""
	}
	return tag
}

func joinPrefix(prefix, s, sep string, squash bool) string {
	if prefix == "" {
		return s
	}
	if squash {
		return prefix
	}
	return fmt.Sprintf("%s%s%s", prefix, sep, s)
}

func joinByCamelCase(s, sep string) string {
	ss := splitByCamelCase(s)
	return strings.Join(ss, sep)
}

func nameToCamelCase(s string) string {
	ss := splitByCamelCase(s)
	if len(ss) == 0 {
		return s
	}
	return strings.ToLower(ss[0]) + strings.Join(ss[1:], "")
}

func splitByCamelCase(s string) (ss []string) {
	if s == "" {
		return nil
	}
	if !utf8.ValidString(s) {
		return []string{s}
	}
	var runes [][]rune
	lastClass := 0
	class := 0
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			class = 1
		case unicode.IsUpper(r):
			class = 2
		case unicode.IsDigit(r):
			class = 3
		default:
			class = 4
		}
		if class == lastClass {
			runes[len(runes)-1] = append(runes[len(runes)-1], r)
		} else {
			runes = append(runes, []rune{r})
		}
		lastClass = class
	}
	for i := 0; i < len(runes)-1; i++ {
		if unicode.IsUpper(runes[i][0]) && unicode.IsLower(runes[i+1][0]) {
			runes[i+1] = append([]rune{runes[i][len(runes[i])-1]}, runes[i+1]...)
			runes[i] = runes[i][:len(runes[i])-1]
		}
	}
	for _, s := range runes {
		if len(s) > 0 {
			ss = append(ss, string(s))
		}
	}
	return ss
}
