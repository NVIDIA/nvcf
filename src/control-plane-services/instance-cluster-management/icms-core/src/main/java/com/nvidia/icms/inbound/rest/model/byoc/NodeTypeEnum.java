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
package com.nvidia.icms.inbound.rest.model.byoc;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.extern.slf4j.Slf4j;

@Schema(description = "Node type")
@Slf4j
public enum NodeTypeEnum {

    // If any values is not present then default value will be "DEFAULT"
    @JsonProperty("SINGLE")
    SINGLE("SINGLE"),

    @JsonProperty("MULTI")
    MULTI("MULTI");

    private final String nodeType;

    NodeTypeEnum(String nodeType) {
        this.nodeType = nodeType;
    }

    @Override
    public String toString() {
        return nodeType;
    }

    public static Set<NodeTypeEnum> toNodeTypeEnum(@Nullable InstanceTypeUsageEnum instanceTypeUsage) {
        if (instanceTypeUsage == null) {
            return Set.of(NodeTypeEnum.SINGLE);
        }

        switch (instanceTypeUsage) {
            case CONTAINER -> {
                return Set.of(NodeTypeEnum.SINGLE);
            }

            case DEFAULT -> {
                return Set.of(NodeTypeEnum.SINGLE, NodeTypeEnum.MULTI);
            }
            default -> {
                return Set.of(NodeTypeEnum.SINGLE);
            }
        }
    }
}
