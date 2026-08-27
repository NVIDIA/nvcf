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

package configutil

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"reflect"
)

func GetTLSConfigFromBase64(certBase64 string, keyBase64 string, insecureSkipVerify bool) (*tls.Config, error) {
	certPem, err := base64.StdEncoding.DecodeString(certBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode cert base64: %v", err)
	}

	keyPem, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key base64: %v", err)
	}

	cert, err := tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		return nil, fmt.Errorf("failed to create key pair: %v", err)
	}

	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: insecureSkipVerify,
	}, nil
}

func StructToMap(input interface{}) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	v := reflect.ValueOf(input)
	t := reflect.TypeOf(input)

	if v.Kind() == reflect.Pointer {
		v = v.Elem()
		t = t.Elem()
	}

	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected a struct, got %s", v.Kind())
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		// Only export the field if it has a `mapstructure` tag
		mapstructureTag := field.Tag.Get("mapstructure")
		if mapstructureTag == "" || mapstructureTag == "-" {
			continue
		}

		// Handle pointer values
		if value.Kind() == reflect.Pointer && !value.IsNil() {
			value = value.Elem()
		}

		// Recursively convert nested structs
		if value.Kind() == reflect.Struct {
			nestedMap, err := StructToMap(value.Interface())
			if err != nil {
				return nil, err
			}
			result[mapstructureTag] = nestedMap
		} else {
			result[mapstructureTag] = value.Interface()
		}
	}

	return result, nil
}
