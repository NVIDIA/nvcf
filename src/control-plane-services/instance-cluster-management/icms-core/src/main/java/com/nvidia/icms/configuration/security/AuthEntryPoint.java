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
package com.nvidia.icms.configuration.security;

import com.nvidia.icms.errors.IcmsAuthenticationException;
import com.nvidia.icms.outbound.exception.ApiKeysException;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.IOException;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.security.core.AuthenticationException;
import org.springframework.security.oauth2.server.resource.web.BearerTokenAuthenticationEntryPoint;
import org.springframework.security.web.AuthenticationEntryPoint;
import org.springframework.stereotype.Component;
import org.springframework.web.servlet.HandlerExceptionResolver;

@Slf4j
@Component
public class AuthEntryPoint implements AuthenticationEntryPoint {

    // Using the default BearerTokenAuthenticationEntryPoint which populates the
    // WWW-Authenticate header
    private static final AuthenticationEntryPoint defaultAuthenticationEntryPoint =
            new BearerTokenAuthenticationEntryPoint();

    @Autowired
    @Qualifier("handlerExceptionResolver")
    private HandlerExceptionResolver exceptionResolver;

    /*
    This method will get executed whenever any exception is thrown from security layer
    We need to make sure that all the exceptions MUST be child of AuthenticationException
    else Spring boot will cast it into generic authentication error: "Full authentication is required to access this resource"
     */
    @Override
    public void commence(
            HttpServletRequest request, HttpServletResponse response,
            AuthenticationException authException)
            throws IOException, ServletException {

        log.error("Request - {} {}, SIS AuthEntryPoint - Authentication failure",
                  request.getMethod(),
                  request.getRequestURI(),
                  authException);

        String errorDetails = authException.getMessage();

        try {
            defaultAuthenticationEntryPoint.commence(request, response, authException);
        } catch (IOException | ServletException exception) {
            log.error("Error handling failure in defaultAuthenticationEntryPoint", exception);
            errorDetails = exception.getMessage();
        }

        // All custom exceptions thrown from security layer which has httpStatus other than 401 and 403 should have handling here
        if (authException instanceof ApiKeysException) {
            exceptionResolver.resolveException(request, response, null,
                    new ApiKeysException(errorDetails));
            return;
        }

        // Fall back logic for all authentication errors, returned response will be 401
        IcmsAuthenticationException icmsAuthenticationException =
                new IcmsAuthenticationException(errorDetails);

        exceptionResolver.resolveException(request, response, null, icmsAuthenticationException);
    }
}
