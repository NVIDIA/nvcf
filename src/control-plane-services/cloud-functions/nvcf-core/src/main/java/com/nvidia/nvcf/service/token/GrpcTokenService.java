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
package com.nvidia.nvcf.service.token;

import static com.nvidia.nvcf.util.NvcfConstants.ENC_KEY_NAME;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_VERSION_ID;

import com.fasterxml.jackson.annotation.JsonEnumDefaultValue;
import com.fasterxml.jackson.annotation.JsonIgnore;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@RequiredArgsConstructor
@Service
public class GrpcTokenService {

    private final Tracer tracer;
    private final JwtService jwtService;
    private final JsonMapper jsonMapper;

    public record NvcfIssuedToken(@NonNull UUID functionId,
                                  @NonNull UUID functionVersionId,
                                  @NonNull Instant issuedAt,
                                  @NonNull TokenType type) {

        public static final Duration VALIDITY = Duration.ofHours(3);

        public enum TokenType {
            @JsonEnumDefaultValue UNKNOWN, WORKER
        }

        @JsonIgnore
        public boolean isValid(TokenType expectedType) {
            if (type == expectedType) {
                return issuedAt.plus(tokenValidityPeriod()).isAfter(Instant.now());
            }
            return false;
        }

        @JsonIgnore
        public Duration tokenValidityPeriod() {
            return VALIDITY;
        }
    }

    @SneakyThrows
    public String issueToken(UUID functionId, UUID functionVersionId, TokenType type) {
        var modelToken = new NvcfIssuedToken(functionId, functionVersionId, Instant.now(), type);
        var encodedArtifactToken = jsonMapper.writeValueAsString(modelToken);
        return jwtService.encryptWithKeysetName(ENC_KEY_NAME, encodedArtifactToken);
    }

    @SneakyThrows
    public NvcfIssuedToken validateToken(String encryptedToken, TokenType type) {
        try {
            var encodedModelToken = jwtService.decrypt(encryptedToken);
            var token = jsonMapper.readValue(encodedModelToken, NvcfIssuedToken.class);
            if (!token.isValid(type)) {
                throw new IllegalStateException();
            }
            NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                    SPAN_TAG_FUNCTION_ID, token.functionId().toString(),
                    SPAN_TAG_FUNCTION_VERSION_ID, token.functionVersionId().toString()));
            return token;
        } catch (Exception e) {
            throw new ForbiddenException("invalid token");
        }
    }
}
