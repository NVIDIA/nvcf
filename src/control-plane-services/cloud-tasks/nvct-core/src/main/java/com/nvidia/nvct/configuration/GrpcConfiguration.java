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
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import net.devh.boot.grpc.server.advice.GrpcAdvice;
import net.devh.boot.grpc.server.advice.GrpcExceptionHandler;
import net.devh.boot.grpc.server.security.authentication.BearerAuthenticationReader;
import net.devh.boot.grpc.server.security.authentication.GrpcAuthenticationReader;
import net.devh.boot.grpc.server.security.interceptors.DefaultAuthenticatingServerInterceptor;
import net.devh.boot.grpc.server.security.interceptors.ExceptionTranslatingServerInterceptor;
import net.devh.boot.grpc.server.serverfactory.GrpcServerConfigurer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.core.AuthenticationException;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.web.ErrorResponseException;

@Configuration(proxyBeanMethods = false)
@GrpcAdvice
public class GrpcConfiguration {

    @Bean
    public GrpcAuthenticationReader authenticationReader() {
        return new BearerAuthenticationReader(BearerTokenAuthenticationToken::new);
    }

    @Bean
    public ExceptionTranslatingServerInterceptor exceptionTranslatingServerInterceptor() {
        return new ExceptionTranslatingServerInterceptor();
    }

    @Bean
    public DefaultAuthenticatingServerInterceptor authenticatingServerInterceptor(
            final GrpcAuthenticationReader authenticationReader) {
        // pass-through auth manager, since we need to auth in a non-blocking context
        return new DefaultAuthenticatingServerInterceptor(authentication -> authentication,
                                                          authenticationReader);
    }

    @Bean
    public GrpcServerConfigurer keepAliveServerConfigurer() {
        return serverBuilder -> {
            if (serverBuilder instanceof NettyServerBuilder sb) {
                sb.permitKeepAliveWithoutCalls(true);
            }
        };
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
}
