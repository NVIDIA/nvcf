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
package com.nvidia.icms.outbound.fnds.model;

import static java.lang.String.format;

import java.util.EnumSet;
import lombok.Getter;
import lombok.NonNull;

public enum FndsStages {
    STAGE_BUILDING("building", "InstanceBuilding"), // not used by SIS
    STAGE_READY("ready", "InstanceReady"),
    STAGE_DESTROYED("destroyed", "InstanceDestroyed"),
    STAGE_PENDING("pending", "InstancePending"), // not used by SIS
    REQUESTING_TERMINATION("requestingTermination", "InstanceRequestingTermination");

    private final String stage;
    @Getter
    private final String cloudEventType;

    FndsStages(String stage, String cloudEventType) {
        this.stage = stage;
        this.cloudEventType = cloudEventType;
    }

    @Override
    public String toString() {
        return this.stage;
    }

    public static FndsStages fromText(@NonNull String val) {
        return EnumSet.allOf(FndsStages.class)
                .stream()
                .filter(e -> e.stage.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalStateException(format("Unsupported enum %s.", val)));
    }
}
