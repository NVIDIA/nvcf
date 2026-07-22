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
package com.nvidia.nvct.service.token;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.grpc.auth.AuthHeaderPassthroughServerHttpRequest;
import com.nvidia.nvct.grpc.auth.SecurityExpression;
import jakarta.servlet.http.HttpServletRequest;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.authentication.AuthenticationManager;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class GrpcAuthService {

    private final AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver;

    /**
     * grpc calls do not flow through the regular spring security filters, so we have to check manually
     */
    public Authentication validateBearer(
            BearerTokenAuthenticationToken bearer,
            String... authorities) {
        AuthenticationManager authenticationManager = authenticationManagerResolver.resolve(
                new AuthHeaderPassthroughServerHttpRequest(bearer));
        Authentication authenticate = authenticationManager.authenticate(bearer);
        boolean hasAnyAuthority = new SecurityExpression(authenticate).hasAnyAuthority(authorities);
        if (!hasAnyAuthority) {
            throw new ForbiddenException("missing task privileges");
        }
        return authenticate;
    }
}
