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

package enforce

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

const (
	// needsEnforceLabel is set on all objects that need to be mutated by the
	// enforcement webhook.
	needsEnforceLabel = "nvca.nvcf.nvidia.io/needs-enforce"
	// enforcementsAnnotation contains all enforcements applied to the object.
	enforcementsAnnotation = "nvca.nvcf.nvidia.io/enforcements"

	trueVal  = "true"
	falseVal = "false"
)

func SetMetadata(obj metav1.Object, enforcements featureflag.Attributes) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}

	if enforcements.Empty() {
		labels[needsEnforceLabel] = falseVal
		return
	}

	labels[needsEnforceLabel] = trueVal

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
		obj.SetAnnotations(annotations)
	}

	enfStrs := make([]string, enforcements.Len())
	i := 0
	for ek, ev := range enforcements.Iter() {
		enfStrs[i] = fmt.Sprintf("%s=%s", ek, ev)
		i++
	}
	sort.Strings(enfStrs)
	annotations[enforcementsAnnotation] = strings.Join(enfStrs, ",")
}

func IsEnforcementLabelSet(obj metav1.Object) bool {
	labels := obj.GetLabels()
	return labels != nil && labels[needsEnforceLabel] == trueVal
}

func GetEnforcements(obj metav1.Object) featureflag.Attributes {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return featureflag.Attributes{}
	}

	enforcementsStr := annotations[enforcementsAnnotation]
	if enforcementsStr == "" {
		return featureflag.Attributes{}
	}

	split := strings.Split(enforcementsStr, ",")
	out := make(map[string]string, len(split))
	for _, s := range split {
		ek, ev, _ := strings.Cut(s, "=")
		if ev == "" {
			ev = "true"
		}
		out[ek] = ev
	}
	return featureflag.NewAttributes(out)
}
