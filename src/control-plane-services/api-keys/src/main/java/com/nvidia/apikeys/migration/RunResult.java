/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.nvidia.apikeys.migration;

import java.util.Map;

/**
 * Outcome of one migration run and the number of keys per result.
 */
public record RunResult(RunOutcome outcome, Map<KeyResult, Integer> counts) {

    public RunResult {
        counts = Map.copyOf(counts);
    }

    public int count(KeyResult result) {
        return counts.getOrDefault(result, 0);
    }

    /**
     * A full pass that found nothing left to copy. The task stops scheduling itself after it.
     */
    public boolean isClean() {
        return outcome == RunOutcome.COMPLETED
                && count(KeyResult.MIGRATED) == 0
                && count(KeyResult.SKIPPED) == 0
                && count(KeyResult.FAILED) == 0;
    }
}
