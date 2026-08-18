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
package com.nvidia.nvcf.service.function;

import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.GpuSpecificationsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Lookup service for function deployment and GPU specs. Builds {@link FunctionDeploymentContext}
 * by reading from repositories and resolving GPU specs from the gpu_specifications table.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionDeploymentLookupService {

    public static final String MESG_DEPLOYMENT_NOT_FOUND =
            "Deployment id '%s': Deployment not found";
    public static final String MESG_FUNCTION_DEPLOYMENT_NOT_FOUND =
            "Function version '%s': Deployment not found";

    private final FunctionsDeploymentRepository functionsDeploymentRepository;
    private final GpuSpecificationsRepository gpuSpecificationRepository;

    /**
     * Fetches all deployments for the NCA from functions_deployment_v2, resolves GPU specs
     * from gpu_specifications table, and returns a stream of
     * {@link FunctionDeploymentContext}.
     */
    public Stream<FunctionDeploymentContext> getFunctionDeploymentContextByNcaId(String ncaId) {
        List<FunctionDeploymentEntity> deployments;
        try (var stream = lookupDeploymentsByNcaId(ncaId)) {
            deployments = stream.toList();
        }
        return deployments.stream()
                .map(this::getDeploymentContext);
    }

    /**
     * Builds FunctionDeploymentContext from a deployment entity using the gpu_specifications
     * table.
     */
    public FunctionDeploymentContext getDeploymentContext(FunctionDeploymentEntity deployment) {
        var gpuSpecs = findGpuSpecsByNcaIdAndDeploymentId(
                deployment.getNcaId(), deployment.getDeploymentId());
        return new FunctionDeploymentContext(deployment, gpuSpecs);
    }

    /**
     * Load deployment by deploymentId with GPU specs.
     */
    public Optional<FunctionDeploymentContext> getDeploymentContextByDeploymentId(
            UUID deploymentId) {
        return lookupDeploymentByDeploymentId(deploymentId)
                .map(this::getDeploymentContext);
    }

    /**
     * Load deployment by deploymentId and returns the deployment entity.
     */
    public FunctionDeploymentEntity getFunctionDeploymentEntityOrThrow(UUID deploymentId) {
        return getDeploymentContextByDeploymentId(deploymentId)
                .map(FunctionDeploymentContext::deployment)
                .orElseThrow(() -> {
                    var mesg = format(MESG_DEPLOYMENT_NOT_FOUND, deploymentId);
                    log.error(mesg);
                    return new NotFoundException(mesg);
                });
    }

    /**
     * Load deployment by deploymentId with GPU specs, or throw.
     */
    public FunctionDeploymentContext getDeploymentContextByDeploymentIdOrThrow(UUID deploymentId) {
        return getDeploymentContextByDeploymentId(deploymentId)
                .orElseThrow(() -> {
                    var mesg = format(MESG_DEPLOYMENT_NOT_FOUND, deploymentId);
                    log.error(mesg);
                    return new NotFoundException(mesg);
                });
    }

    /**
     * Load deployment and GPU specs by function version id.
     */
    public Optional<FunctionDeploymentContext> getDeploymentContextByVersionId(
            UUID functionVersionId) {
        return lookupDeploymentByVersionId(functionVersionId)
                .map(this::getDeploymentContext);
    }

    /**
     * Load deployment and GPU specs by function version id, or throw if not found.
     */
    public FunctionDeploymentContext getDeploymentContextByVersionIdOrThrow(
            UUID functionVersionId) {
        return getDeploymentContextByVersionId(functionVersionId).orElseThrow(() -> {
            var mesg = format(MESG_FUNCTION_DEPLOYMENT_NOT_FOUND, functionVersionId);
            log.error(mesg);
            return new NotFoundException(mesg);
        });
    }

    public Optional<FunctionDeploymentEntity> lookupDeploymentByDeploymentId(UUID deploymentId) {
        return functionsDeploymentRepository.getByDeploymentId(deploymentId);
    }


    public Optional<FunctionDeploymentEntity> lookupDeploymentByVersionId(UUID functionVersionId) {
        return functionsDeploymentRepository.getByKeyFunctionVersionId(functionVersionId);
    }

    public List<GpuSpecificationEntity> findGpuSpecsByNcaIdAndDeploymentId(String ncaId,
                                                                           UUID deploymentId) {
        try (var stream = gpuSpecificationRepository.findAllByKeyNcaIdAndKeyDeploymentId(
                ncaId, deploymentId)) {
            return stream.toList();
        }
    }

    public Stream<FunctionDeploymentEntity> lookupDeploymentsByNcaId(String ncaId) {
        return functionsDeploymentRepository.findAllByNcaId(ncaId);
    }
}
