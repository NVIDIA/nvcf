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

package com.nvidia.apikeys.facade;

import com.nvidia.apikeys.dto.authz.AuthzRequest;
import com.nvidia.apikeys.dto.authz.AuthzResponse;
import com.nvidia.apikeys.services.AuthzService;
import com.nvidia.apikeys.validators.AuthzRequestValidator;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class AuthzFacade {

    private final AuthzRequestValidator requestValidator;
    private final IntrospectionFacade introspectionFacade;
    private final AuthzService authzService;


    public AuthzResponse runPolicy(String namespace, String ruleName, AuthzRequest request) {
        var requestVo = requestValidator.validate(namespace, ruleName, request);
        try {
            var introspectionResponse = introspectionFacade.introspect(
                    requestVo.getIntrospectionRequest());
            var result = authzService.evaluatePolicy(
                    requestVo.getIntrospectionRequest().getAudienceServiceId(),
                    introspectionResponse);
            return AuthzResponse.builder()
                    .ruleName(ruleName)
                    .namespace(namespace)
                    .result(result)
                    .build();
        } catch (Exception e) {
            log.error("key introspections failure: {}", e.getMessage());
            return AuthzResponse.builder()
                    .ruleName(ruleName)
                    .namespace(namespace)
                    .result(AuthzResponse.Result.builder().allowed(false).build())
                    .build();
        }

    }
}
