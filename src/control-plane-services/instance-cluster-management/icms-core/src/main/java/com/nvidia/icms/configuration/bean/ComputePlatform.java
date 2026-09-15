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

import jakarta.validation.constraints.NotBlank;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

/**
 * A first-party compute platform whose clusters are handled outside the default BYOC
 * (customer) flow, identified by a fixed cluster group.
 *
 * <p>The registry of platforms is empty by default — OSS / ICMS deployments serve only
 * BYOC clusters, so every cluster group is treated as BYOC. Deployments that operate a
 * first-party platform supply entries via {@code icms.compute-platforms} as configuration data.
 */
@Data
@NoArgsConstructor
@AllArgsConstructor
@Builder
public class ComputePlatform {

    /**
     * The provider/backend identity of this platform. Used by {@code ComputePlatformService}
     * to classify a provider or backend value as a compute platform, so it MUST equal the
     * serialized {@code CloudProvider}/{@code ResourceProvider}/{@code ClusterProviderEnum}
     * value. Matching is case-sensitive.
     */
    @NotBlank
    private String name;

    /**
     * The {@code clusterGroupName} stamped on clusters that belong to this platform. This is the
     * sole lookup key used by {@code ComputePlatformService} to match a cluster to a platform;
     * a blank value would make every cluster fall back to BYOC, hence the {@link NotBlank} guard.
     */
    @NotBlank
    private String clusterGroupName;

    /** The ancillary {@code clusterGroupId} stamped on clusters; not used for platform lookup. */
    private String clusterGroupId;
}
