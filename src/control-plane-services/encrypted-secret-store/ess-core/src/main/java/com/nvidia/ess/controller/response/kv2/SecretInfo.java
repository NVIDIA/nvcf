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
package com.nvidia.ess.controller.response.kv2;

import io.swagger.v3.oas.annotations.media.ArraySchema;
import io.swagger.v3.oas.annotations.media.Schema;
import java.util.List;
import java.util.Map;
import lombok.Builder;
import lombok.Data;

@Builder
@Data
public class SecretInfo {
    // fields populated depend on the API called
    @Schema(description = "Populated only when query_type=FETCH_SECRET or undefined. "
            + "Size of the JSON-serialized representation for the top level .data JSON object cannot exceed 32KB")
    private Map<String, Object> data;
    private SecretVersionMetadata metadata;

    @ArraySchema(maxItems = Integer.MAX_VALUE, schema = @Schema(type = "string", pattern = ".*",
            maxLength = Integer.MAX_VALUE - 1))
    private List<String> keys;
}
