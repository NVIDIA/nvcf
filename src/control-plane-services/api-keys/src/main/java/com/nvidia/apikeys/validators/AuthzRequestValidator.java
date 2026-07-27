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

package com.nvidia.apikeys.validators;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.authz.ApiKeyInput;
import com.nvidia.apikeys.dto.authz.AuthzRequest;
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.vo.PolicyEvaluationRequestVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Pattern;
import lombok.RequiredArgsConstructor;
import org.apache.commons.collections4.MapUtils;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class AuthzRequestValidator {

    private static final Pattern SUPPORTED_POLICY_NAME = Pattern.compile("[\\w-]+\\.allow");

    private final NakProperties nakProperties;

    public PolicyEvaluationRequestVo validate(
            String namespace, String ruleName, AuthzRequest request) {
        assertValidRuleName(ruleName);

        IntrospectionRequest introspectionRequest = IntrospectionRequest.builder()
                .key(getApiKey(request))
                .audienceServiceId(resolveAudienceServiceId(namespace))
                .build();

        return PolicyEvaluationRequestVo.builder()
                .namespace(namespace)
                .policyName(ruleName)
                .introspectionRequest(introspectionRequest)
                .build();
    }

    private String resolveAudienceServiceId(String namespace) {
        Map<String, String> map = MapUtils.emptyIfNull(nakProperties.getServiceIdMap());
        String aud = map.get(namespace);
        if (StringUtils.isEmpty(aud)) {
            throw new BadRequestException(
                    "Namespace '" + namespace + "' is not configured");
        }
        return aud;
    }

    private String getApiKey(AuthzRequest request) {
        return Optional.ofNullable(request)
                .map(AuthzRequest::getApiKeyInput)
                .map(ApiKeyInput::getApiKey)
                .filter(StringUtils::isNotEmpty)
                .orElseThrow( () -> new BadRequestException("Api key is not provided"));
    }

    private void assertValidRuleName(String policyName) {
        if (policyName == null || !SUPPORTED_POLICY_NAME.matcher(policyName).matches()) {
            throw new BadRequestException("Rule name is not supported: " + policyName);
        }
    }
}
