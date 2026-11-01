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

package k8sutil

import (
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ParseAnnotations returns a parsed map[string]string from
// ',' separated "key=value" pairs without enforcing any
// k8s label restrictions
func ParseAnnotations(annosStr string) (map[string]string, error) {
	if annosStr == "" {
		return nil, nil
	}

	kvPairs := strings.Split(annosStr, ",")
	kvMap := make(map[string]string, len(kvPairs))
	for _, s := range kvPairs {
		kv := strings.SplitN(s, "=", 2)
		if len(kv) > 1 {
			kvMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		} else {
			kvMap[strings.TrimSpace(kv[0])] = ""
		}
	}
	errList := apivalidation.ValidateAnnotations(kvMap, field.NewPath("metadata.annotations"))
	return kvMap, errList.ToAggregate()
}
