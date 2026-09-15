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

import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_INVOKE_FUNCTION;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.configuration.exceptions.InvalidInvocationException;
import com.nvidia.nvcf.proto.ClientInvokeRequest;
import com.nvidia.nvcf.proto.ClientInvokeResponse;
import com.nvidia.nvcf.proto.ClientInvokeResponse.FunctionVersion;
import com.nvidia.nvcf.proto.InvocationGrpc.InvocationImplBase;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService.FunctionContext;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.List;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.web.ErrorResponseException;

@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcInvocationService extends InvocationImplBase {

    private final GrpcAuthService grpcAuthService;
    private final AccountService accountService;
    private final FunctionInvocationValidationService functionInvocationValidationService;

    @Override
    public void authClientInvocation(
            ClientInvokeRequest request,
            StreamObserver<ClientInvokeResponse> responseObserver) {
        validateInvokeFunctionProxyAuth();

        // get the client's auth (not the proxy's)
        var authentication = validateInvokeFunctionAuth(request);

        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = request.hasFunctionVersionId() ?
                UUID.fromString(request.getFunctionVersionId()) : null;
        var ncaId = request.hasTargetNcaId() ? request.getTargetNcaId()
                : accountService.getNcaId(authentication);
        var functions = lookupAndValidateAccess(authentication,
                ncaId,
                functionId,
                functionVersionId);

        // picking the first function version for function level info.
        // all function versions will be of the same function.
        var first = functions.getFirst();
        var versions = functions.stream()
                .map(FunctionInvocationValidationService.FunctionContext::targetFunction)
                .map(functionEntity -> {
                    var inferenceUrl = getNormalizedInferenceUrl(functionEntity.getInferenceUrl());
                    return FunctionVersion.newBuilder()
                            .setFunctionVersionId(functionEntity
                                                          .getFunctionVersionId()
                                                          .toString())
                            .setDefaultInvocationPath(inferenceUrl)
                            .setHasRateLimit(functionEntity.getRateLimit() != null
                                                     && !functionEntity.getRateLimit().isEmpty())
                            .setSyncCheck(functionEntity.getRateLimit() != null
                                                  && functionEntity.getRateLimit().getSyncCheck()
                                    != null
                                                  && functionEntity.getRateLimit().getSyncCheck())
                            .build();
                })
                .toList();

        responseObserver.onNext(ClientInvokeResponse.newBuilder()
                                        .setFunctionId(request.getFunctionId())
                                        .setClientAuthSubject(first.subject())
                                        .setClientNcaId(first.ncaId())
                                        .addAllFunctionVersions(versions)
                                        .build());
        responseObserver.onCompleted();
    }

    /**
     * we allowed people to add invalid paths such as "v2/invoke". normalize them to "/v2/invoke"
     */
    @VisibleForTesting
    static String getNormalizedInferenceUrl(String inferenceUrl) {
        if (inferenceUrl.isEmpty()) {
            return inferenceUrl;
        }
        if (inferenceUrl.startsWith("/")) {
            return inferenceUrl;
        }
        return "/" + inferenceUrl;
    }

    private void validateInvokeFunctionProxyAuth() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("missing token");
        }

        grpcAuthService.validateBearer(bearer, "invocation:check_invocation");
    }

    private Authentication validateInvokeFunctionAuth(ClientInvokeRequest clientInvokeRequest) {
        var bearer = new BearerTokenAuthenticationToken(
                clientInvokeRequest.getClientAuthorizationToken());

        // check if super admin invocation
        if (clientInvokeRequest.hasTargetNcaId()) {
            return grpcAuthService.validateBearer(bearer, ADMIN_SCOPE_INVOKE_FUNCTION);
        }
        return grpcAuthService.validateBearer(bearer, SCOPE_INVOKE_FUNCTION,
                                              "apikey:" + SCOPE_INVOKE_FUNCTION);
    }

    /**
     * Wraps the lookupAndValidateAccess call to include ncaId in any thrown exceptions.
     * This allows the gRPC error handler to include ncaId in the error metadata for tracing.
     */
    private List<FunctionContext> lookupAndValidateAccess(
            Authentication authentication,
            String ncaId,
            UUID functionId,
            UUID functionVersionId) {
        try {
            return functionInvocationValidationService.lookupAndValidateAccess(
                    authentication, ncaId, functionId, functionVersionId);
        } catch (ErrorResponseException e) {
            log.error("Failed to lookup and validate access for ncaId={}, functionId={}, " +
                            "functionVersionId={}", ncaId, functionId, functionVersionId);
            throw new InvalidInvocationException(ncaId, e);
        }
    }

}
