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
package com.nvidia.icms.service.createInstances;

import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Null;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@Builder
public class DestinationClusterData {
    @NotNull
    private String clusterGroupId;

    @NotNull
    private String clusterGroupName;

    @NotNull
    private String ncaId;

    @NotNull
    private Set<String> authorizedNcaIds;

    @Nullable
    private String clusterName;

    @Nullable
    private String clusterId;

    @Nullable
    private String region;

    @NotNull
    private Set<GpuV5Udt> gpusV5;

    @NotNull
    private Map<String, String> queueUrlByGpuNameMap;


    public DestinationClusterData(@NotNull ClustersByAuthorizedAccountsEntity clusterAccounts, @NotNull Map<String, String> queueUrlByGpuNameMap) {
        this.clusterGroupId = clusterAccounts.getClusterGroupId();
        this.clusterGroupName =clusterAccounts.getClusterGroupName();
        this.ncaId = clusterAccounts.getNcaId();
        this.authorizedNcaIds = new HashSet<>(clusterAccounts.getAuthorizedNcaIds());
        this.clusterName = clusterAccounts.getClusterName();
        this.clusterId = clusterAccounts.getKey().getClusterId();
        this.region = null;
        this.gpusV5 =  NvcaConverter.getGpusV5(clusterAccounts);
        this.queueUrlByGpuNameMap = queueUrlByGpuNameMap;
    }

    public DestinationClusterData(@NotNull ClusterByGroupIdAndIdEntity cluster, @NotNull Map<String, String> queueUrlByGpuNameMap) {
        this.clusterGroupId = cluster.getKey().getClusterGroupId();
        this.clusterGroupName = cluster.getClusterGroupName();
        this.ncaId = cluster.getNcaId();
        this.authorizedNcaIds = new HashSet<>(cluster.getAuthorizedNcaIds());
        this.clusterName = cluster.getClusterName();
        this.clusterId = cluster.getKey().getClusterId();
        this.region = cluster.getRegion();
        this.gpusV5 = cluster.getGpusV5();
        this.queueUrlByGpuNameMap = queueUrlByGpuNameMap;
    }
}
