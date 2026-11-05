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
package com.nvidia.icms.errors;

import java.util.Collection;
import java.util.List;
import lombok.Getter;

/**
 * A cluster registration would remove instance types that still have active instances.
 * The error response carries the instance types and instances, so NVCA can report them
 * without parsing the message.
 */
@Getter
public class IcmsActiveInstancesConflictException extends IcmsConflictException {

    /**
     * {@code reason} of the {@link ActiveInstancesConflictErrorResponse} for this conflict.
     */
    public static final String REASON_ACTIVE_INSTANCES_ON_REMOVED_INSTANCE_TYPES =
            "ACTIVE_INSTANCES_ON_REMOVED_INSTANCE_TYPES";

    private final List<String> instanceTypes;
    private final List<String> activeInstanceIds;

    public IcmsActiveInstancesConflictException(
            String message,
            Collection<String> instanceTypes,
            Collection<String> activeInstanceIds) {
        super(message);
        this.instanceTypes = instanceTypes.stream().sorted().toList();
        this.activeInstanceIds = activeInstanceIds.stream().sorted().toList();
    }
}
