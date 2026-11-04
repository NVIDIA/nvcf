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
package com.nvidia.icms.configuration.bean;

import java.util.HashMap;
import java.util.Map;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

/**
 * Configuration properties for instanceType to GPU count mappings.
 * Allows configuration of instance type to GPU count mappings via application.yaml.
 */
@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "icms.instance-type-configuration")
@Data
@Slf4j
public class InstanceTypeConfigurationProperties {

    private Map<String, Integer> instanceTypeToGpuCount = new HashMap<>();
    private int defaultGpuCount = 1;

    /**
     * Gets the GPU count for the specified instance type.
     * Returns the default GPU count if the instance type is not configured.
     *
     * @param instanceType The instance type to get GPU count for
     * @return The GPU count for the instance type, or default value if not found
     */
    public int getGpuCountForInstanceType(String instanceType) {
        Integer gpuCount = instanceTypeToGpuCount.get(instanceType);
        if (gpuCount == null) {
            log.warn("class: InstanceTypeConfigurationProperties, instanceType '{}' not found in GPU count mapping, using default value: {}",
                     instanceType, defaultGpuCount);
            return defaultGpuCount;
        }
        return gpuCount;
    }
}
