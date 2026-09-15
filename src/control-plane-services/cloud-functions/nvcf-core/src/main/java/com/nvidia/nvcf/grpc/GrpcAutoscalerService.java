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
package com.nvidia.nvcf.grpc;

import static com.nvidia.nvcf.proto.AutoscalerGrpc.AutoscalerImplBase;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_AUTOSCALER_AUTH;
import static com.nvidia.nvcf.util.NvcfUtils.parseUuid;

import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.proto.AutoscalerRequest;
import com.nvidia.nvcf.proto.AutoscalerResponse;
import com.nvidia.nvcf.proto.AutoscalingConfiguration;
import com.nvidia.nvcf.proto.DeploymentConfigurationRequest;
import com.nvidia.nvcf.proto.DeploymentConfigurationResponse;
import com.nvidia.nvcf.service.autoscaler.AutoscalerService;
import com.nvidia.nvcf.service.function.AutoscalingConfigurationMapper;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.HashMap;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.util.CollectionUtils;

@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcAutoscalerService extends AutoscalerImplBase {
    private static final String MESG_FETCH_AUTOSCALING_CONFIG =
            "Function id '{}', version '{}': Fetch autoscaling config";
    private static final String MESG_AUTOSCALE_FUNCTION =
            "Function id '{}', version '{}': Autoscale function";
    private static final String MESG_MISSING_TOKEN =
            "Function id '%s', version id '%s': missing token";

    private final GrpcAuthService grpcAuthService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final AutoscalingConfigurationMapper autoscalingConfigurationMapper;
    private final AutoscalerService autoscalerService;

    @Override
    public void requestDeploymentConfiguration(
            DeploymentConfigurationRequest request,
            StreamObserver<DeploymentConfigurationResponse> responseObserver) {
        var functionId = request.getFunctionId();
        var versionId = request.getFunctionVersionId();

        validateAuth(functionId, versionId, SCOPE_AUTOSCALER_AUTH);
        log.info(MESG_FETCH_AUTOSCALING_CONFIG, functionId, versionId);

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionIdOrThrow(
                parseUuid("functionVersionId", request.getFunctionVersionId(),
                          request.getFunctionId(), request.getFunctionVersionId()));
        Map<String, AutoscalingConfiguration> config = new HashMap<>();
        if (!CollectionUtils.isEmpty(deploymentContext.gpuSpecs())) {
            for (GpuSpecificationEntity entry : deploymentContext.gpuSpecs()) {
                if (entry.getAutoscalingConfig() != null) {
                    var gpuAutoscalingConfig =
                            autoscalingConfigurationMapper.toAutoscalingConfiguration(
                                    entry.getAutoscalingConfig());
                    config.put(
                            entry.getKey().getGpuSpecificationId().toString(),
                            gpuAutoscalingConfig);
                }
            }
        }

        var builder = DeploymentConfigurationResponse.newBuilder();
        builder.putAllConfigs(config);
        responseObserver.onNext(builder.build());
        responseObserver.onCompleted();
    }

    @Override
    public void autoscaleFunction(
            AutoscalerRequest request,
            StreamObserver<AutoscalerResponse> responseObserver) {
        var functionId = request.getFunctionId();
        var versionId = request.getFunctionVersionId();

        validateAuth(functionId, versionId, SCOPE_AUTOSCALER_AUTH);
        log.info(MESG_AUTOSCALE_FUNCTION, functionId, versionId);

        var response = autoscalerService.scaleInstances(request);
        responseObserver.onNext(response);
        responseObserver.onCompleted();
    }

    private void validateAuth(String functionId, String versionId, String... scopes) {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            var mesg = MESG_MISSING_TOKEN.formatted(functionId, versionId);
            log.error(mesg);
            throw new UnauthorizedException(mesg);
        }

        grpcAuthService.validateBearer(bearer, scopes);
    }
}
