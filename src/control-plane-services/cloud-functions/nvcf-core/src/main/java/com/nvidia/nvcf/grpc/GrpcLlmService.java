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
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.grpc.auth.SecurityExpression;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmInvokeResponse;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerRequest;
import com.nvidia.nvcf.proto.llm_gateway.AuthLlmWorkerResponse;
import com.nvidia.nvcf.proto.llm_gateway.LlmGatewayGrpc.LlmGatewayImplBase;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.service.apikeys.LlmApiKeyService;
import com.nvidia.nvcf.service.function.FunctionLlmService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService.FunctionContext;
import com.nvidia.nvcf.service.serviceaccount.ServiceAccountService;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import io.grpc.stub.StreamObserver;
import java.util.Optional;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;

@Slf4j
@GrpcService
public class GrpcLlmService extends LlmGatewayImplBase {

    private static final String MESG_INVALID_LLM_CONFIG =
            "Function id '%s', version '%s': Invalid LLM config";
    private static final String MESG_INVALID_ROUTING_KEY =
            "Function id '%s': Invalid routing key - model must be prefixed with"
                    + " a valid function ID (UUID)";
    private static final String API_KEY_PREFIX = "nvapi-";

    private final GrpcAuthService grpcAuthService;
    private final GrpcTokenService grpcTokenService;
    private final AccountService accountService;
    private final FunctionMapperService functionMapperService;
    private final FunctionLlmService functionLlmService;
    private final FunctionInvocationValidationService functionInvocationValidationService;
    private final ServiceAccountService serviceAccountService;
    private final LlmApiKeyService llmApiKeyService;
    private final boolean accountTokenRateLimitEnabled;

    public GrpcLlmService(
            GrpcAuthService grpcAuthService,
            GrpcTokenService grpcTokenService,
            AccountService accountService,
            FunctionMapperService functionMapperService,
            FunctionLlmService functionLlmService,
            FunctionInvocationValidationService functionInvocationValidationService,
            ServiceAccountService serviceAccountService,
            LlmApiKeyService llmApiKeyService,
            @Value("${nvcf.account-token-rate-limit-enabled:false}") boolean accountTokenRateLimitEnabled) {
        this.grpcAuthService = grpcAuthService;
        this.grpcTokenService = grpcTokenService;
        this.accountService = accountService;
        this.functionMapperService = functionMapperService;
        this.functionLlmService = functionLlmService;
        this.functionInvocationValidationService = functionInvocationValidationService;
        this.serviceAccountService = serviceAccountService;
        this.llmApiKeyService = llmApiKeyService;
        this.accountTokenRateLimitEnabled = accountTokenRateLimitEnabled;
    }

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
        var accountRateLimit = resolveAccountRateLimit(authentication, ncaId);

        var responseBuilder = AuthLlmInvokeResponse.newBuilder()
                .setRoutingKey(request.getRoutingKey())
                .setClientAuthSubject(first.subject())
                .putAuthContext("ncaId", first.ncaId());

        resolvedPriority.ifPresent(p -> responseBuilder.setPriority(p.intValue()));
        accountRateLimit.ifPresent(rl -> {
            if (rl.inputTokenRateLimit() != null) {
                responseBuilder.setAccountInputTokenRateLimit(rl.inputTokenRateLimit());
            }
            if (rl.outputTokenRateLimit() != null) {
                responseBuilder.setAccountOutputTokenRateLimit(rl.outputTokenRateLimit());
            }
        });

        for (var model : functionModels) {
            var modelSpecBuilder = AuthLlmInvokeResponse.ModelSpec.newBuilder();
            var llmConfig = model.getLlmConfig();
            if (llmConfig != null && llmConfig.getUris() != null) {
                modelSpecBuilder.addAllUris(llmConfig.getUris());
            }
            if (llmConfig != null && llmConfig.getTokenRateLimit() != null) {
                modelSpecBuilder.setTokenRateLimit(llmConfig.getTokenRateLimit());
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

    // The API-key path's rate limit comes from the LLM-specific apikey.llm_allow evaluation
    // (LlmApiKeyService), attached to its ApiKeyValidationResult. JWT has no such attribute,
    // so it calls ServiceAccountService directly by ncaId instead.
    private Optional<ApiKeyValidationResult.RateLimitAttributes> resolveAccountRateLimit(
            Authentication authentication,
            String ncaId) {
        if (authentication instanceof JwtAuthenticationToken) {
            return Optional.ofNullable(serviceAccountService.getTieredRateLimit(ncaId));
        }
        if (!(authentication.getPrincipal() instanceof OAuth2AuthenticatedPrincipal principal)) {
            return Optional.empty();
        }
        if (principal.getAttribute(ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE)
                instanceof ApiKeyValidationResult result
                && result.accountTokenRateLimit() != null) {
            return Optional.of(result.accountTokenRateLimit());
        }
        // An older policy deploy that doesn't populate accountTokenRateLimit yet - fall back
        // to the standalone lookup instead of silently skipping enforcement during a
        // policy/nvcf-core rollout skew.
        return Optional.ofNullable(serviceAccountService.getTieredRateLimit(ncaId));
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

    // Bypasses the shared AuthenticationManagerResolver (always apikey.allow) for API keys
    // specifically, so only this RPC can reach the rate-limit-carrying llm_allow evaluation.
    private Authentication validateInvokeFunctionAuth(
            AuthLlmInvokeRequest request) {
        var token = request.getClientAuthorizationToken();
        if (accountTokenRateLimitEnabled && token.startsWith(API_KEY_PREFIX)) {
            return authenticateApiKeyForLlmInvocation(token);
        }
        var bearer = new BearerTokenAuthenticationToken(token);
        return grpcAuthService.validateBearer(bearer, SCOPE_INVOKE_FUNCTION,
                                              "apikey:" + SCOPE_INVOKE_FUNCTION);
    }

    private Authentication authenticateApiKeyForLlmInvocation(String apiKey) {
        var result = llmApiKeyService.resolveForLlmInvocation(apiKey);
        var authentication = result.toBearerTokenAuthentication(apiKey);
        if (!new SecurityExpression(authentication)
                .hasAnyAuthority(SCOPE_INVOKE_FUNCTION, "apikey:" + SCOPE_INVOKE_FUNCTION)) {
            throw new ForbiddenException("missing requested authorities");
        }
        return authentication;
    }
}
