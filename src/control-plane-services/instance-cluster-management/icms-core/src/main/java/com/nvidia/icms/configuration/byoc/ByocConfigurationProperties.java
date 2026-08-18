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
package com.nvidia.icms.configuration.byoc;

import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "icms.byoc")
@Data
@Slf4j
public class ByocConfigurationProperties {
    private String env;
    private String queueNameFormat;
    private int instanceBatchCount;
    private int cloudHealthTtlForByocInSec;
    private int clusterExpiryTimeInDays;
    private boolean clusterHealthMonitorTaskEnabled;
    private List<Map<String, String>> skipHealthCheckClusters;
    private boolean modelCachingEnabled;
    private Set<String> terminateCloudFailedInstancesFromClusters;
    private boolean authorizedNcaIdUpdateEnabled;
    private Long cacheByteDivisionFactor;
    private Long cacheBytesBuffer;
    private Long cacheReservedSpace;
    private boolean autoTerminationOfInstancesFromUnhealthyCloudEnabled;
    private int timeForAutoTerminatingInstancesFromUnhealthyCloudInHours = 24;
}
