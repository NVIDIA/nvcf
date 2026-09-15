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

import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.proto.RateLimitGrpc.RateLimitImplBase;
import com.nvidia.nvcf.proto.RateLimitPolicyRequest;
import com.nvidia.nvcf.proto.RateLimitPolicyResponse;
import com.nvidia.nvcf.proto.RateLimitPolicyResponse.RateLimitConfig;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.token.GrpcAuthService;
import io.grpc.stub.StreamObserver;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;


@Slf4j
@GrpcService
@RequiredArgsConstructor
public class GrpcRateLimiterService extends RateLimitImplBase {

    private static final String MESG_NO_RATELIMIT = "Function %s version %s does not have a rate limit policy";

    private final GrpcAuthService grpcAuthService;
    private final FunctionLookupService functionLookupService;

    @Override
    public void rateLimitPolicy(RateLimitPolicyRequest request,
                                StreamObserver<RateLimitPolicyResponse> responseObserver) {
        validateAuth();
        var functionId = UUID.fromString(request.getFunctionId());
        var functionVersionId = UUID.fromString(request.getFunctionVersionId());
        var builder = RateLimitPolicyResponse.newBuilder();
        functionLookupService.lookupUsingFunctionIdAndVersionId(functionId, functionVersionId)
                .map(FunctionEntity::getRateLimit)
                .map(rateLimit -> {
                    if (rateLimit.isEmpty()) {
                        var mesg = format(MESG_NO_RATELIMIT, functionId, functionVersionId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    var config = RateLimitConfig.newBuilder();
                    if (rateLimit.getExemptedNcaIds() != null) {
                        config.addAllExcludedNcaIds(rateLimit.getExemptedNcaIds());
                    }
                    if (rateLimit.getPerNcaIdRate() != null) {
                        rateLimit.getPerNcaIdRate().forEach((ncaId, rate) -> {
                            var perNcaIdConfigs = RateLimitPolicyResponse.RateLimitConfig.PerNcaIdConfigs.newBuilder()
                                .setNcaId(ncaId)
                                .setRate(rate)
                                .build();
                            config.addPerNcaIdConfigs(perNcaIdConfigs);
                        });
                    }
                    if (rateLimit.getRate() != null) {
                        config.setRate(rateLimit.getRate());
                    }
                    if (rateLimit.getPerUserRate() != null) {
                        config.setPerUserRate(rateLimit.getPerUserRate());
                    }
                    return builder.setConfig(config).build();
                }).orElseThrow(() -> new NotFoundException(format(MESG_NO_RATELIMIT, functionId, functionVersionId)));

        responseObserver.onNext(builder.build());
        responseObserver.onCompleted();
    }

    private void validateAuth() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (!(authentication instanceof BearerTokenAuthenticationToken bearer)) {
            throw new UnauthorizedException("missing token");
        }

        grpcAuthService.validateBearer(bearer, "ratelimit:check_invocation");
    }

}
