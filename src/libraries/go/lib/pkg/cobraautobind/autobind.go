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

// Package cobraautobind registers Cobra CLI flags from a struct shape and binds them to Viper.
//
// TODO(NVCF-9201): unit-test coverage is below the lib module threshold; add tests for this package.
package cobraautobind

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	tagMapStructure = "mapstructure"
	tagDefault      = "default"
	tagUsage        = "usage"
	tagFlag         = "flag"
)

// AutobindFlagsFromStruct registers CLI flags from a struct and binds them to Viper.
//
// Tags supported:
//   - mapstructure: key for unmarshalling and flag name if flag is not set
//   - flag: override CLI flag name
//   - default: default value (string literal)
//   - usage: help message
func AutobindFlagsFromStruct(cmd *cobra.Command, v *viper.Viper, config any) error {
	return recursiveAutobind(cmd, v, config, "")
}

func recursiveAutobind(cmd *cobra.Command, v *viper.Viper, config any, prefix string) error {
	val := reflect.ValueOf(config)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return ErrConfigMustBeNonNil
	}

	val = val.Elem()
	typ := val.Type()
	if typ.Kind() != reflect.Struct {
		return ErrConfigMustBeStruct
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldVal := val.Field(i)

		if !field.IsExported() {
			continue
		}

		flagName := field.Tag.Get(tagFlag)
		if flagName == "" {
			flagName = field.Tag.Get(tagMapStructure)
			if flagName == "" {
				flagName = strings.ToLower(field.Name)
			}
		}

		if flagName == "-" {
			continue
		}

		fullFlag := flagName
		if prefix != "" {
			fullFlag = prefix + "." + flagName
		}

		usage := field.Tag.Get(tagUsage)
		defStr := field.Tag.Get(tagDefault)

		if field.Type.Kind() == reflect.Struct {
			if err := recursiveAutobind(cmd, v, fieldVal.Addr().Interface(), fullFlag); err != nil {
				return err
			}
			continue
		}

		if cmd.Flags().Lookup(fullFlag) != nil {
			continue
		}

		switch field.Type.Kind() {
		case reflect.String:
			cmd.Flags().String(fullFlag, defStr, usage)

		case reflect.Int:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, strconv.Atoi)
			if err != nil {
				return err
			}
			cmd.Flags().Int(fullFlag, def, usage)

		case reflect.Int32:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, func(s string) (int32, error) {
				i, err := strconv.ParseInt(s, 10, 32)
				return int32(i), err
			})
			if err != nil {
				return err
			}
			cmd.Flags().Int32(fullFlag, def, usage)

		case reflect.Int64:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, func(s string) (int64, error) {
				return strconv.ParseInt(s, 10, 64)
			})
			if err != nil {
				return err
			}
			cmd.Flags().Int64(fullFlag, def, usage)

		case reflect.Uint16:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, func(s string) (uint16, error) {
				i, err := strconv.ParseUint(s, 10, 16)
				return uint16(i), err
			})
			if err != nil {
				return err
			}
			cmd.Flags().Uint16(fullFlag, def, usage)

		case reflect.Bool:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, strconv.ParseBool)
			if err != nil {
				return err
			}
			cmd.Flags().Bool(fullFlag, def, usage)

		case reflect.Float32, reflect.Float64:
			def, err := parseDefault(defStr, field.Type.String(), fullFlag, func(s string) (float64, error) {
				return strconv.ParseFloat(s, 64)
			})
			if err != nil {
				return err
			}
			cmd.Flags().Float64(fullFlag, def, usage)

		case reflect.Slice:
			if field.Type.Elem().Kind() != reflect.String {
				continue
			}
			cmd.Flags().StringSlice(fullFlag, nil, usage)

		default:
			return &UnsupportedFieldTypeError{
				fieldType: field.Type.String(),
				fullFlag:  fullFlag,
			}
		}

		if err := v.BindPFlag(fullFlag, cmd.Flags().Lookup(fullFlag)); err != nil {
			return err
		}
	}

	return nil
}

func parseDefault[T any](defStr, fieldType, fullFlag string, parseFunc func(string) (T, error)) (T, error) {
	var zero T
	if defStr == "" {
		return zero, nil
	}

	parsed, err := parseFunc(defStr)
	if err != nil {
		return zero, &InvalidDefaultError{
			fieldType:       fieldType,
			fullFlag:        fullFlag,
			conversionError: err,
		}
	}
	return parsed, nil
}
