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

import static com.nvidia.nvcf.persistence.function.entity.FunctionType.DEFAULT;
import static com.nvidia.nvcf.persistence.function.entity.FunctionType.STREAMING;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_INVOKE_FUNCTION;

import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.proto.ProxyAuthRequest;
import com.nvidia.nvcf.proto.ProxyAuthResponse;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion.BackendType;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion.FunctionType;
import com.nvidia.nvcf.proto.ProxyGrpc.ProxyImplBase;
import com.nvidia.nvcf.rest.function.management.dto.BackendEnum;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.Objects;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;

@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcProxyService extends ProxyImplBase {

    private final GrpcAuthService grpcAuthService;
    private final AccountService accountService;
    private final FunctionInvocationValidationService functionInvocationValidationService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;

    @Override
    public void authStatefulWork(
            ProxyAuthRequest request,
            StreamObserver<ProxyAuthResponse> responseObserver) {
        validateInvokeFunctionProxyAuth();
        // get the client's auth (not the proxy's)
        var authentication = validateInvokeFunctionAuth(request);

        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = request.hasFunctionVersionId() ?
                UUID.fromString(request.getFunctionVersionId()) : null;
        var ncaId = accountService.getNcaId(authentication);
        var functions = functionInvocationValidationService.lookupAndValidateAccess(
                authentication, ncaId, functionId, functionVersionId);

        // picking the first function version for function level info.
        // all function versions will be of the same function.
        var first = functions.getFirst();
        var versions = functions.stream()
                .map(FunctionInvocationValidationService.FunctionContext::targetFunction)
                .map(functionEntity -> {
                    var builder = FunctionVersion.newBuilder()
                            .setFunctionVersionId(
                                    functionEntity.getFunctionVersionId()
                                            .toString())
                            .setType(switch (Objects.requireNonNullElse(
                                    functionEntity.getFunctionType(), DEFAULT)) {
                                case DEFAULT -> FunctionType.DEFAULT;
                                case STREAMING -> FunctionType.STREAMING;
                                case LLM -> FunctionType.LLM;
                            })
                            .setHasRateLimit(functionEntity.getRateLimit() != null
                                            && !functionEntity.getRateLimit().isEmpty())
                            .setSyncCheck(functionEntity.getRateLimit() != null
                                    && functionEntity.getRateLimit().getSyncCheck() != null
                                    && functionEntity.getRateLimit().getSyncCheck());
                    if (functionEntity.getFunctionType() == STREAMING) {
                        var deploymentContext = functionDeploymentLookupService
                                .getDeploymentContextByVersionIdOrThrow(
                                        functionEntity.getFunctionVersionId());
                        var gpuSpecs = deploymentContext.gpuSpecs();
                        var isGfn = gpuSpecs.stream()
                                .map(GpuSpecificationEntity::getBackend)
                                .anyMatch(BackendEnum.GFN.name()::equals);
                        if (isGfn) {
                            builder.setBackendType(BackendType.GFN);
                        } else {
                            builder.setBackendType(BackendType.UNKNOWN);
                        }
                        return builder.build();
                    }
                    return builder.build();
                })
                .toList();
        responseObserver.onNext(ProxyAuthResponse.newBuilder()
                                        .setFunctionId(request.getFunctionId())
                                        .setClientAuthSubject(first.subject())
                                        .setClientNcaId(first.ncaId())
                                        .addAllFunctionVersions(versions)
                                        .build());
        responseObserver.onCompleted();
    }

    private void validateInvokeFunctionProxyAuth() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("missing token");
        }

        grpcAuthService.validateBearer(bearer, "proxy:invoke_function");
    }

    private Authentication validateInvokeFunctionAuth(ProxyAuthRequest proxyAuthRequest) {
        var bearer = new BearerTokenAuthenticationToken(
                proxyAuthRequest.getClientAuthorizationToken());

        return grpcAuthService.validateBearer(bearer, SCOPE_INVOKE_FUNCTION,
                                              "apikey:" + SCOPE_INVOKE_FUNCTION);
    }
}
