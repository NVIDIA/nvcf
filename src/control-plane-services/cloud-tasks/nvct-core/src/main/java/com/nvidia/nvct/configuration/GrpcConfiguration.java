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
package com.nvidia.nvct.configuration;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import io.grpc.Status;
import io.grpc.Status.Code;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.grpc.server.GlobalServerInterceptor;
import org.springframework.grpc.server.advice.GrpcAdvice;
import org.springframework.grpc.server.advice.GrpcExceptionHandler;
import org.springframework.grpc.server.security.AuthenticationProcessInterceptor;
import org.springframework.grpc.server.security.BearerTokenAuthenticationExtractor;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.authorization.AuthorizationDecision;
import org.springframework.security.core.AuthenticationException;
import org.springframework.web.ErrorResponseException;

@Configuration(proxyBeanMethods = false)
@GrpcAdvice
public class GrpcConfiguration {

    @Bean
    @GlobalServerInterceptor
    public AuthenticationProcessInterceptor bearerTokenServerInterceptor() {
        // pass-through auth manager and permit-all authorization, since the services
        // validate the bearer token themselves in a non-blocking context
        return new AuthenticationProcessInterceptor(authentication -> authentication,
                                                    new BearerTokenAuthenticationExtractor(),
                                                    (authentication, call) -> new AuthorizationDecision(true));
    }

    @GrpcExceptionHandler
    public Status handleErrorResponseException(ErrorResponseException e) {
        var code = switch (e.getStatusCode().value()) {
            case 400 -> Code.INTERNAL;
            case 401 -> Code.UNAUTHENTICATED;
            case 403 -> Code.PERMISSION_DENIED;
            case 404 -> Code.NOT_FOUND;
            case 429, 502, 503, 504 -> Code.UNAVAILABLE;
            default -> Code.UNKNOWN;
        };
        return Status.fromCode(code).withDescription(e.getBody().getDetail()).withCause(e);
    }

    @GrpcExceptionHandler
    public Status handleException(AccessDeniedException e) {
        return handleErrorResponseException(new ForbiddenException(e.getMessage(), e.getCause()));
    }

    @GrpcExceptionHandler
    public Status handleException(AuthenticationException e) {
        return handleErrorResponseException(
                new UnauthorizedException(e.getMessage(), e.getCause()));
    }

    @GrpcExceptionHandler
    public Status handleUnmappedException(Exception e) {
        // Preserve the INTERNAL status expected by workers for unmapped exceptions.
        return Status.INTERNAL.withDescription("There was a server error trying to handle an exception")
                .withCause(e);
    }
}
