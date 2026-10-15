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
package com.nvidia.nvct.grpc;


import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvct.proto.SkywayAuthRequest;
import com.nvidia.nvct.proto.SkywayAuthResponse;
import com.nvidia.nvct.proto.SkywayGrpc;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.instance.InstanceService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
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
public class GrpcSkywayService extends SkywayGrpc.SkywayImplBase {
    private final GrpcAuthService grpcAuthService;
    private final AccountService accountService;
    private final InstanceService instanceService;
    private final TaskService taskService;

    private static final String MESG_INVALID_NCA_ID = "Permission denied for the task ID: %s";
    private static final String MESG_NO_ACTIVE_INSTANCE = "No active instances for the task ID: %s";
    private static final String SKYWAY_AUTH_SCOPE = "skyway:auth";

    @Override
    public void authGetLogs(
            SkywayAuthRequest request,
            StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_LIST_TASKS);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }


    @Override
    public void authExecuteCommand(
            SkywayAuthRequest request,
            StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_LAUNCH_TASK);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }

    @Override
    public void authListInstances(
            SkywayAuthRequest request,
            StreamObserver<SkywayAuthResponse> responseObserver) {
        validateAuth();
        var authentication = validateClientAuth(request, SCOPE_LIST_TASKS);
        var authResponse = buildAuthResponse(request, authentication);
        responseObserver.onNext(authResponse);
        responseObserver.onCompleted();
    }

    private void validateAuth() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("Unauthorized");
        }

        grpcAuthService.validateBearer(bearer, SKYWAY_AUTH_SCOPE);
    }

    private Authentication validateClientAuth(
            SkywayAuthRequest clientInvokeRequest,
            String targetScope) {
        var bearer = new BearerTokenAuthenticationToken(
                clientInvokeRequest.getClientAuthorizationToken());

        // check if super admin invocation
        if (clientInvokeRequest.hasTargetNcaId()) {
            return grpcAuthService.validateBearer(bearer, "admin:" + targetScope);
        }
        return grpcAuthService.validateBearer(bearer, targetScope);
    }

    private SkywayAuthResponse buildAuthResponse(
            SkywayAuthRequest request, Authentication authentication) {
        var ncaId = request.hasTargetNcaId() ? request.getTargetNcaId()
                : accountService.getNcaId(authentication);
        var authResponseBuilder = SkywayAuthResponse.newBuilder()
                .setTaskId(request.getTaskId())
                .setClientNcaId(ncaId)
                .setClientAuthSubject(authentication.getName());

        try {
            var taskId = UUID.fromString(request.getTaskId());
            var taskEntity = taskService.fetchTask(taskId);
            if (!taskEntity.getNcaId().equals(ncaId)) {
                var errMsg = MESG_INVALID_NCA_ID.formatted(taskId);
                log.error(errMsg);
                throw new ForbiddenException(errMsg);
            }

            var instances = instanceService
                    .getInstances(taskEntity)
                    .orElseThrow(
                            () -> new NotFoundException(MESG_NO_ACTIVE_INSTANCE.formatted(taskId)))
                    .stream()
                    .map(instance -> SkywayAuthResponse.Instance.newBuilder()
                            .setInstanceId(instance.getInstanceId())
                            .setLocation(instance.getLocation())
                            .setState(instance.getInstanceState() == null ? "UNKNOWN" :
                                              instance.getInstanceState().toString())
                            .build())
                    .toList();
            authResponseBuilder.addAllInstances(instances);
        } catch (NotFoundException e) {
            log.error(e.getMessage(), e);
            throw new ForbiddenException(e.getMessage());
        }
        return authResponseBuilder.build();
    }
}
