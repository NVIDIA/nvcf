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

import static com.nvidia.icms.errors.IcmsActiveInstancesConflictException.REASON_ACTIVE_INSTANCES_ON_REMOVED_INSTANCE_TYPES;

import io.swagger.v3.oas.annotations.media.Schema;
import java.util.List;
import lombok.Getter;
import lombok.Setter;

/**
 * API response body of the 409 that cluster registration and registration validation
 * return when the request would remove instance types that still have active instances.
 * It keeps the human-readable {@code error} message of {@link CustomErrorResponse}, so
 * clients that only read {@code error} keep working, and adds the same facts as fields.
 */
@Getter
@Setter
@Schema(description = "Conflict body. Every conflict sets error; a registration that would "
        + "remove instance types with active instances also sets the fields below.")
public class ActiveInstancesConflictErrorResponse extends CustomErrorResponse {

    @Schema(description = "Machine-readable cause of the conflict.",
            allowableValues = {REASON_ACTIVE_INSTANCES_ON_REMOVED_INSTANCE_TYPES})
    private String reason;

    @Schema(description = "Instance types that the request leaves out but that still have "
            + "active instances on this cluster, sorted.")
    private List<String> instanceTypes;

    @Schema(description = "IDs of the active instances on those instance types, sorted.")
    private List<String> activeInstanceIds;
}
