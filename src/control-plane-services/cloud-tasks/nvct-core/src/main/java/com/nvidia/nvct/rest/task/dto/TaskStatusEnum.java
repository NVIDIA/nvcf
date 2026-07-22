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
package com.nvidia.nvct.rest.task.dto;

import static java.lang.String.format;

import java.util.EnumSet;
import lombok.NonNull;

public enum TaskStatusEnum {

    QUEUED("QUEUED"),
    LAUNCHED("LAUNCHED"),
    RUNNING("RUNNING"),
    ERRORED("ERRORED"),
    CANCELED("CANCELED"),
    EXCEEDED_MAX_RUNTIME_DURATION("EXCEEDED_MAX_RUNTIME_DURATION"),
    EXCEEDED_MAX_QUEUED_DURATION("EXCEEDED_MAX_QUEUED_DURATION"),
    COMPLETED("COMPLETED");

    private final String name;

    TaskStatusEnum(String name) {
        this.name = name;
    }

    @Override
    public String toString() {
        return this.name;
    }

    public static TaskStatusEnum fromText(@NonNull String val) {
        return EnumSet.allOf(TaskStatusEnum.class)
                .stream()
                .filter(e -> e.name.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalArgumentException(format("Unsupported enum '%s'",
                                                                       val)));
    }

}
