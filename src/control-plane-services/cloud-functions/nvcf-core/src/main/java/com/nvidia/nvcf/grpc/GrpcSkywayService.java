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

import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.proto.SkywayAuthRequest;
import com.nvidia.nvcf.proto.SkywayAuthResponse;
import com.nvidia.nvcf.proto.SkywayGrpc.SkywayImplBase;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;

@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcSkywayService extends SkywayImplBase {

    private final GrpcAuthService grpcAuthService;
    private final AccountService accountService;
    private final InstanceService instanceService;
    private final FunctionLookupService functionLookupService;

    private static final String SKYWAY_AUTH_SCOPE = "skyway:auth";

    @Override
    public void authGetLogs(SkywayAuthRequest request,
                            StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_LIST_FUNCTIONS);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }


    @Override
    public void authExecuteCommand(SkywayAuthRequest request,
                                   StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_DEPLOY_FUNCTION);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }

    @Override
    public void authListInstances(SkywayAuthRequest request,
                                  StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_LIST_FUNCTIONS);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }

    private void validateAuth() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("missing token");
        }

        grpcAuthService.validateBearer(bearer, SKYWAY_AUTH_SCOPE);
    }

    private Authentication validateClientAuth(SkywayAuthRequest clientInvokeRequest,
                                              String targetScope) {
        var bearer = new BearerTokenAuthenticationToken(
                clientInvokeRequest.getClientAuthorizationToken()
        );

        // check if super admin invocation
        if (clientInvokeRequest.hasTargetNcaId()) {
            return grpcAuthService.validateBearer(bearer, "admin:" + targetScope);
        }
        return grpcAuthService.validateBearer(bearer, targetScope, "apikey:" + targetScope);
    }

    private SkywayAuthResponse buildAuthResponse(
            SkywayAuthRequest request, Authentication authentication) {
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        var ncaId = request.hasTargetNcaId() ? request.getTargetNcaId()
                : accountService.getNcaId(authentication);
        var authResponseBuilder = SkywayAuthResponse.newBuilder()
                .setFunctionId(request.getFunctionId())
                .setFunctionVersionId(request.getFunctionVersionId())
                .setClientNcaId(ncaId)
                .setClientAuthSubject(authentication.getName());

        try {
            // Make sure the account having access on the target function.
            functionLookupService
                    .lookupUsingAccountIdAndFunctionIdAndVersionIdOrThrow(
                            ncaId, functionId, functionVersionId);
            var instances = instanceService
                    .getInstanceRequests(ncaId, functionId, functionVersionId)
                    .stream()
                    .filter(instanceRequest ->
                                    StringUtils.isNotBlank(instanceRequest.getInstanceId()))
                    .map(instance -> SkywayAuthResponse.Instance.newBuilder()
                            .setInstanceId(instance.getInstanceId())
                            .setLocation(instance.getLaunchedAvailabilityZone())
                            .setActive(instance.getInstanceState() != null
                                               && instance.getInstanceState().isRunning())
                            .build())
                    .toList();
            authResponseBuilder.addAllInstances(instances);
        } catch (NotFoundException e) {
            throw new ForbiddenException(e.getMessage(), e);
        }
        return authResponseBuilder.build();
    }
}
