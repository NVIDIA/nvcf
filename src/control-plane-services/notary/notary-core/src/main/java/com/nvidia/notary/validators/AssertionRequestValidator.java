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
package com.nvidia.notary.validators;

import com.nvidia.notary.config.NotaryProperties;
import com.nvidia.notary.services.JwtResolver;
import com.nvidia.notary.vo.AssertionRequestVo;
import com.nvidia.notary.web.dto.AssertionRequest;
import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.servlet.http.HttpServletRequest;
import java.util.List;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class AssertionRequestValidator {

    private final NotaryProperties notaryProperties;
    private final AudiencesValidator audiencesValidator;
    private final JwtResolver jwtResolver;

    /**
     * Validates assertion request parameters.
     *
     * @param requestBody Deserialized request body.
     * @param request Reference to current request.
     *
     * @return valid assertion request parameters ready for issuing token.
     */
    public AssertionRequestVo validate(AssertionRequest requestBody, HttpServletRequest request) {
        if (requestBody == null || request == null || request.getContentLength() == 0) {
            throw new BadRequestException("Content length must be greater than zero");
        }

        if (request.getContentLength() > notaryProperties.getMaxAssertionsRequestSize()) {
            throw new BadRequestException("Content length is greater than the maximum allowed: "
                                          + notaryProperties.getMaxAssertionsRequestSize());
        }

        Map<String, Object> data = requestBody.getData();
        if (data == null || data.isEmpty()) {
            throw new BadRequestException("Request data is empty");
        }

        List<String> validatedAudiences = audiencesValidator.getValidatedAudiences(
                requestBody.getAudienceServiceIds());

        Jwt callerToken = jwtResolver.getCallerToken();

        return new AssertionRequestVo(callerToken, validatedAudiences, data);
    }

}
