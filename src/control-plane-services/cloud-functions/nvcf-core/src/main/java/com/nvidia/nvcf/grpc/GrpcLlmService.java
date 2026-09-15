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

import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LLM_CHECK_INVOCATION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LLM_CHECK_WORKER;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeResponse;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerResponse;
import com.nvidia.nvcf.proto.llm_gateway.LlmGatewayGrpc.LlmGatewayImplBase;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.FunctionLlmService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService.FunctionContext;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.stub.StreamObserver;
import java.util.Optional;
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
public class GrpcLlmService extends LlmGatewayImplBase {

    private static final String MESG_INVALID_LLM_CONFIG =
            "Function id '%s', version '%s': Invalid LLM config";
    private static final String MESG_INVALID_ROUTING_KEY =
            "Function id '%s': Invalid routing key - model must be prefixed with"
                    + " a valid function ID (UUID)";

    private final GrpcAuthService grpcAuthService;
    private final GrpcTokenService grpcTokenService;
    private final AccountService accountService;
    private final FunctionMapperService functionMapperService;
    private final FunctionLlmService functionLlmService;
    private final FunctionInvocationValidationService functionInvocationValidationService;

    @Override
    public void authLlmInvocation(
            AuthLlmInvokeRequest request,
            StreamObserver<AuthLlmInvokeResponse> responseObserver) {
        validateLlmGatewayAuth(SCOPE_LLM_CHECK_INVOCATION);

        var authentication = validateInvokeFunctionAuth(request);

        final UUID functionId;
        try {
            functionId = UUID.fromString(request.getRoutingKey());
        } catch (IllegalArgumentException e) {
            // Caller-supplied routing key (model prefix). A non-UUID is a bad request,
            // not an INTERNAL error; map it to INVALID_ARGUMENT so the gateway returns 400.
            var mesg = MESG_INVALID_ROUTING_KEY.formatted(request.getRoutingKey());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        var ncaId = accountService.getNcaId(authentication);
        var functions = functionInvocationValidationService.lookupAndValidateAccess(
                authentication, ncaId, functionId, null);

        var first = functions.getFirst();
        var functionModels = functionMapperService.toFunctionModels(
                first.targetFunction().getModelSpecs());
        var resolvedPriority = resolvePriority(first);

        var responseBuilder = AuthLlmInvokeResponse.newBuilder()
                .setRoutingKey(request.getRoutingKey())
                .setClientAuthSubject(first.subject())
                .putAuthContext("ncaId", first.ncaId());

        resolvedPriority.ifPresent(p -> responseBuilder.setPriority(p.intValue()));

        for (var model : functionModels) {
            var modelSpecBuilder = AuthLlmInvokeResponse.ModelSpec.newBuilder();
            var llmConfig = model.getLlmConfig();
            if (llmConfig != null && llmConfig.getUris() != null) {
                modelSpecBuilder.addAllUris(llmConfig.getUris());
            }
            if (llmConfig != null && llmConfig.getTokenRateLimit() != null) {
                modelSpecBuilder.setTokenRateLimit(llmConfig.getTokenRateLimit());
            }
            if (llmConfig != null && llmConfig.getTokenizer() != null) {
                modelSpecBuilder.setTokenizer(llmConfig.getTokenizer());
            }
            if (llmConfig != null && llmConfig.getRoutingMethod() != null) {
                modelSpecBuilder.setRoutingMethod(llmConfig.getRoutingMethod());
            }
            responseBuilder.putModelSpecs(model.getName(), modelSpecBuilder.build());
        }

        responseObserver.onNext(responseBuilder.build());
        responseObserver.onCompleted();
    }

    @Override
    public void authLlmWorker(
            AuthLlmWorkerRequest request,
            StreamObserver<AuthLlmWorkerResponse> responseObserver) {
        validateLlmGatewayAuth(SCOPE_LLM_CHECK_WORKER);

        var workerToken = grpcTokenService.validateToken(request.getWorkerToken(), TokenType.WORKER);
        responseObserver.onNext(AuthLlmWorkerResponse.newBuilder()
                                        .setRoutingKey(workerToken.functionId().toString())
                                        .build());
        responseObserver.onCompleted();
    }

    private Optional<Long> resolvePriority(FunctionContext context) {
        try {
            var llmInvocationConfig = functionMapperService.toLlmInvocationConfigDto(
                    context.targetFunction().getLlmConfig());
            return functionLlmService.resolveInvocationPriority(
                    context.ncaId(), llmInvocationConfig);
        } catch (IllegalStateException exception) {
            log.error(MESG_INVALID_LLM_CONFIG.formatted(
                    context.targetFunction().getFunctionId(),
                    context.targetFunction().getFunctionVersionId()), exception);
            throw exception;
        }
    }

    private void validateLlmGatewayAuth(String requiredScope) {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("missing token");
        }

        grpcAuthService.validateBearer(bearer, requiredScope);
    }

    private Authentication validateInvokeFunctionAuth(
            AuthLlmInvokeRequest request) {
        var bearer = new BearerTokenAuthenticationToken(request.getClientAuthorizationToken());
        return grpcAuthService.validateBearer(bearer, SCOPE_INVOKE_FUNCTION,
                                              "apikey:" + SCOPE_INVOKE_FUNCTION);
    }
}
