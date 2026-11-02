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
 * Outcome of one migration run, the number of keys per result, and the number of owner status
 * rows per result.
 */
public record RunResult(
        RunOutcome outcome, Map<KeyResult, Integer> counts,
        Map<OwnerStatusResult, Integer> ownerStatusCounts) {

    public RunResult {
        counts = Map.copyOf(counts);
        ownerStatusCounts = Map.copyOf(ownerStatusCounts);
    }

    public RunResult(RunOutcome outcome, Map<KeyResult, Integer> counts) {
        this(outcome, counts, Map.of());
    }

    public int count(KeyResult result) {
        return counts.getOrDefault(result, 0);
    }

    public int count(OwnerStatusResult result) {
        return ownerStatusCounts.getOrDefault(result, 0);
    }

    /**
     * A full pass that found nothing left to copy, or a run that found both completion markers.
     * The task stops scheduling itself after it. Skipped keys and owner statuses do not count
     * against it, because no later run can copy them.
     */
    public boolean isClean() {
        if (outcome == RunOutcome.ALREADY_COMPLETED) {
            return true;
        }
        return outcome == RunOutcome.COMPLETED
                && count(KeyResult.MIGRATED) == 0
                && count(KeyResult.FAILED) == 0
                && count(OwnerStatusResult.COPIED) == 0
                && count(OwnerStatusResult.FAILED) == 0;
    }
}
