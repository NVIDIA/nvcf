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
package com.nvidia.nvcf.rest.function.deployment.dto;

import static java.lang.String.format;

import java.util.EnumSet;
import lombok.NonNull;

public enum ScalingStatusEnum {
    // Autoscaler is disabled and legacy S3 is used
    NO_OPERATION ("NO_OPERATION"),
    // Current number of active+pending matches requested or there is no space for scaling
    NO_SCALING_NEEDED ("NO_SCALING_NEEDED"),
    SCALING_UP ("SCALING_UP"),
    SCALING_DOWN ("SCALING_DOWN"),
    // Function is not active or active+pending is not in [min, max]
    NOT_AUTO_SCALABLE ("NOT_AUTO_SCALABLE");
    private final String name;

    ScalingStatusEnum(String name) {
        this.name = name;
    }

    @Override
    public String toString() {
        return this.name;
    }

    public static ScalingStatusEnum fromText(@NonNull String val) {
        return EnumSet.allOf(ScalingStatusEnum.class)
                .stream()
                .filter(e -> e.name.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalStateException(
                        format("Unsupported ScalingStatus enum %s.", val)));
    }
}
