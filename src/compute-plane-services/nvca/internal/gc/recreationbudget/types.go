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

package recreationbudget

import "time"

// These must stay in sync with pkg/nvca/recreation_budget.go's own
// unexported constants of the same values. Not imported directly to avoid
// an import cycle (pkg/nvca -> internal/gc -> internal/gc/recreationbudget
// -> pkg/nvca).
const (
	purposeLabel           = "nvca.nvcf.nvidia.io/purpose"
	purposeValue           = "recreation-budget"
	timestampsKey          = "purgeTimestamps"
	recreationBudgetWindow = 15 * time.Minute
)
