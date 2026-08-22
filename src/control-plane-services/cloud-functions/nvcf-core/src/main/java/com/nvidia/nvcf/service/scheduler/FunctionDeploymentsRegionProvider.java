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
package com.nvidia.nvcf.service.scheduler;

import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

/**
 * Resolves the region that owns a deployment during regional scheduling.
 */
@Slf4j
@Service("functionDeploymentsRegionProvider")
@RefreshScope
public class FunctionDeploymentsRegionProvider {

    private static final String MESG_INVALID_REGION =
            "Current region '%s' must be included in the list of configured regions '%s'";
    private static final String MESG_PROVIDER_INFO =
            "Function deployment reconciliation: Current region '{}', configured regions '{}'";

    private final String currentRegion;
    private final List<String> deploymentReconciliationRegions;
    private final int currentRegionIndex;

    public FunctionDeploymentsRegionProvider(
            @Value("${spring.application.env}") String currentRegion,
            @Value("${nvcf.scheduler.function-deployments.regions:${spring.application.env}}")
            List<String> deploymentReconciliationRegions) {
        this.currentRegion = currentRegion;
        this.deploymentReconciliationRegions = List.copyOf(deploymentReconciliationRegions);
        this.currentRegionIndex = this.deploymentReconciliationRegions.indexOf(currentRegion);
        if (currentRegionIndex < 0) {
            var mesg = MESG_INVALID_REGION.formatted(currentRegion,
                                                     deploymentReconciliationRegions);
            log.error(mesg);
            throw new IllegalStateException(mesg);
        }
        log.info(MESG_PROVIDER_INFO, currentRegion, this.deploymentReconciliationRegions);
    }

    public String currentRegion() {
        return currentRegion;
    }

    public boolean owns(FunctionDeploymentEntity deployment) {
        var functionVersionId = deployment.getKey().getFunctionVersionId();
        var ownerIndex = Math.floorMod(functionVersionId.hashCode(),
                                       deploymentReconciliationRegions.size());
        return ownerIndex == currentRegionIndex;
    }
}
