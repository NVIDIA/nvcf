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

package hostisolation

import (
	"context"

	"k8s.io/client-go/kubernetes"
)

// Stub for host security checks.
// TODO: implement host security checks on a node:
// 1) Is GPU reset configured in VM provider by vfio driver?
// 2) Is GPU firmware write-protected or attestable?
func IsNodeHostSecure(_ context.Context, nodeName string, _ kubernetes.Interface) bool {
	return true
}
