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

package translate

import (
	"math"
	"time"

	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

func ParseMaxRuntimeDuration(maxRuntimeDurationStr string) (time.Duration, error) {
	// If the max runtime duration is not set, use the default max runtime duration of
	// infinity (or near infinite with max duration)
	if maxRuntimeDurationStr == "" {
		return time.Duration(math.MaxInt64), nil
	}

	return translateutil.ParseISO8601Duration(maxRuntimeDurationStr)
}
