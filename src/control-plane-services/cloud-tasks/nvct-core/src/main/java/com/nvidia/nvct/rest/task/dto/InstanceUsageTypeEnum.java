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


// Used to validate the instance-types on which the Task will be launched. This enum
// is used as value for instanceTypeUsage=CONTAINER|DEFAULT query parameter with
// ICMS endpoint. For container-based Tasks, instanceTypeUsage=CONTAINER is used. For
// helm-based Tasks, instanceTypeUsage=DEFAULT is used.
public enum InstanceUsageTypeEnum {
    CONTAINER("CONTAINER"),
    DEFAULT("DEFAULT");

    private final String value;

    InstanceUsageTypeEnum(String value) {
        this.value = value;
    }

    @Override
    public String toString() {
        return value;
    }
}
