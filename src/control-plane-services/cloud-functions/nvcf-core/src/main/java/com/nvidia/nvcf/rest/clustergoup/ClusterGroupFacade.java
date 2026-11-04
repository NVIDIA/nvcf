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
package com.nvidia.nvcf.rest.clustergoup;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ClusterGroupFacade {

    private static final String MESG_CLUSTER_GROUP_NOT_FOUND =
            "Account '%s': Cluster group '%s' not found";
    private static final String MESG_BLANK_PARAMETER =
            "'%s' cannot be empty or null";

    private final IcmsClient icmsClient;

    public ClusterGroupsResponse getClusterGroups(
            String ncaId, InstanceUsageTypeEnum instanceUsage) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return ClusterGroupsResponse.builder()
                .clusterGroups(icmsClient.getClusterGroups(ncaId, instanceUsage)).build();
    }

    public List<ClusterGroup.Gpu> getGpus(
            String ncaId, String clusterGroup, InstanceUsageTypeEnum instanceUsage) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(clusterGroup)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "clusterGroup");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return icmsClient.getClusterGroups(ncaId, instanceUsage).stream()
                .filter(cg -> cg.getName().equals(clusterGroup))
                .findFirst()
                .orElseThrow(() -> {
                    var mesg = MESG_CLUSTER_GROUP_NOT_FOUND.formatted(ncaId, clusterGroup);
                    return new NotFoundException(mesg);
                })
                .getGpus();
    }
}
