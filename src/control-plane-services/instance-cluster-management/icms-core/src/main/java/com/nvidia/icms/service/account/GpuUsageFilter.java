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
package com.nvidia.icms.service.account;

import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import jakarta.validation.constraints.NotNull;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.jetbrains.annotations.Nullable;

@AllArgsConstructor
@NoArgsConstructor
@Data
@Builder
public class GpuUsageFilter {
    @Nullable Set<String> gpuNames;
    @Nullable Set<String> clusterGroupNames;
    @Nullable Set<String> instanceTypes;
    @Nullable Set<String> regionNames;
    @Nullable Set<String> clusterNames;
    @Nullable Set<String> attributes;
    @Nullable InstanceTypeUsageEnum instanceTypeUsageFilter;
    @Builder.Default
    boolean validateCapacity = true;

    public boolean isGpuNameAllowed(String value)  {
        if (gpuNames == null || gpuNames.isEmpty()) {
            return true;
        }

        return doesSetContain(gpuNames, value);
    }

    public boolean isClusterGroupNameAllowed(String value)  {
        if (clusterGroupNames == null || clusterGroupNames.isEmpty()) {
            return true;
        }

        return doesSetContain(clusterGroupNames, value);
    }

    public boolean isInstanceTypeAllowed(String value)  {
        if (instanceTypes == null || instanceTypes.isEmpty()) {
            return true;
        }

        return doesSetContain(instanceTypes, value);
    }

    public boolean isRegionNameAllowed(String value)  {
        if (regionNames == null || regionNames.isEmpty()) {
            return true;
        }

        return doesSetContain(regionNames, value);
    }

    public boolean isClusterNameAllowed(String value)  {
        if (clusterNames == null || clusterNames.isEmpty()) {
            return true;
        }

        return doesSetContain(clusterNames, value);
    }

    public boolean areAttributesAllowed(Set<String> value)  {
        if (attributes == null || attributes.isEmpty()) {
            return true;
        }

        return doesSetContainAnotherSet(attributes, value);
    }

    public boolean isInstanceUsageAllowed(Set<NodeTypeEnum> value)  {
        if (instanceTypeUsageFilter == null) {
            return true;
        }
        Set<NodeTypeEnum> filteredNodeTypes = NodeTypeEnum.toNodeTypeEnum(instanceTypeUsageFilter);
        for (NodeTypeEnum supportedNodeType : value) {
            if (filteredNodeTypes.contains(supportedNodeType)) {
                return true;
            }
        }
        return false;
    }


    private boolean doesSetContain(@NotNull Set<String> source, String value) {
        for(String s: source) {
            if (StringUtils.isBlank(s)) {
                if (StringUtils.isBlank(value)) {
                    return true;
                }
            }
            else if (s.equalsIgnoreCase(value)) {
                return true;
            }
        }

        return false;
    }

    private boolean doesSetContainAnotherSet(@Nullable Set<String> requiredValues,
                                             @Nullable Set<String> providedValues) {
        if ((requiredValues == null || requiredValues.isEmpty()) &&
                (providedValues == null || providedValues.isEmpty())) {
            return true;
        }

        if ((requiredValues == null || requiredValues.isEmpty()) ||
                (providedValues == null || providedValues.isEmpty())) {
            return false;
        }

        for(String s: requiredValues) {
            if (!doesSetContain(providedValues, s)) {
                return false;
            }
        }

        return true;
    }

    public static @Nullable Set<String> getNullOrSet(@Nullable String value) {
        return value == null ? null : Set.of(value);
    }

    // Added custom toString method to get values to print in logging and debugging
    public String toString() {
        return "GpuUsageFilter {" +
                "gpuNames=" + gpuNames +
                ", clusterGroupNames=" + clusterGroupNames +
                ", instanceTypes=" + instanceTypes +
                ", regionNames=" + regionNames +
                ", clusterNames=" + clusterNames +
                ", attributes=" + attributes +
                ", instanceTypeUsageFilter=" + instanceTypeUsageFilter +
                '}';
    }

}
