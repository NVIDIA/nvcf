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

package com.nvidia.apikeys.dto.keysV2;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

class KeyOperationDtosTest {

    private final JsonMapper mapper = JsonUtils.getRequestResponseJsonMapper();
    private final Validator validator = Validation.buildDefaultValidatorFactory().getValidator();

    @Test
    void keyOwnerRequest_deserializesSelectors() throws Exception {
        KeyOwnerKeysOperationRequest request = mapper.readValue("""
                {
                  "operation": "REVOKED",
                  "accounts": ["a1", "a2"],
                  "issuerServices": ["*"],
                  "reason": "Revoke credentials"
                }
                """, KeyOwnerKeysOperationRequest.class);

        assertThat(request.getOperation()).isEqualTo(KeysOperationType.REVOKED);
        assertThat(request.getAccounts()).containsExactly("a1", "a2");
        assertThat(request.getIssuerServices()).containsExactly("*");
        assertThat(request.getReason()).isEqualTo("Revoke credentials");
        assertThat(validator.validate(request)).isEmpty();
    }

    @Test
    void accountAdminRequest_deserializesSelectors() throws Exception {
        AccountAdminKeysOperationRequest request = mapper.readValue("""
                {"operation": "DELETE", "users": ["*"], "issuerServices": ["*"]}
                """, AccountAdminKeysOperationRequest.class);

        assertThat(request.getOperation()).isEqualTo(KeysOperationType.DELETE);
        assertThat(request.getUsers()).containsExactly("*");
        assertThat(validator.validate(request)).isEmpty();
    }

    @Test
    void platformAdminRequest_acceptsBothConfirmationSpellings() throws Exception {
        String selectors = "\"operation\":\"REVOKED\",\"accounts\":[\"*\"],"
                + "\"issuerServices\":[\"*\"],\"users\":[\"*\"]";

        PlatformAdminKeysOperationRequest camel = mapper.readValue(
                "{" + selectors + ",\"confirmPlatformWide\":true}",
                PlatformAdminKeysOperationRequest.class);
        PlatformAdminKeysOperationRequest snake = mapper.readValue(
                "{" + selectors + ",\"confirm_platform_wide\":true}",
                PlatformAdminKeysOperationRequest.class);

        assertThat(camel.getConfirmPlatformWide()).isTrue();
        assertThat(snake.getConfirmPlatformWide()).isTrue();
    }

    @ParameterizedTest
    @CsvSource({"issuerServices,s1", "issuerServices,*", "issuer_services,s1", "issuer_services,*"})
    void allActorsAcceptBothIssuerSelectorSpellings(String field, String value) throws Exception {
        String issuer = "\"" + field + "\":[\"" + value + "\"]";
        var owner = mapper.readValue("{\"operation\":\"DELETE\",\"accounts\":[\"a1\"]," + issuer + "}",
                KeyOwnerKeysOperationRequest.class);
        var account = mapper.readValue("{\"operation\":\"DELETE\",\"users\":[\"u1\"]," + issuer + "}",
                AccountAdminKeysOperationRequest.class);
        var platform = mapper.readValue("{\"operation\":\"DELETE\",\"accounts\":[\"a1\"],"
                + "\"users\":[\"u1\"]," + issuer + "}", PlatformAdminKeysOperationRequest.class);

        assertThat(owner.getIssuerServices()).containsExactly(value);
        assertThat(account.getIssuerServices()).containsExactly(value);
        assertThat(platform.getIssuerServices()).containsExactly(value);
        for (var request : List.of(owner, account, platform)) {
            assertThat(validator.validate(request)).isEmpty();
            var serialized = mapper.readTree(mapper.writeValueAsString(request));
            assertThat(serialized.get("issuerServices").get(0).asString()).isEqualTo(value);
            assertThat(serialized.has("issuer_services")).isFalse();
        }
    }

    @Test
    void requests_rejectMissingOrEmptyRequiredFields() {
        assertThat(validator.validate(new KeyOwnerKeysOperationRequest())).hasSize(3);
        assertThat(validator.validate(new AccountAdminKeysOperationRequest())).hasSize(3);
        assertThat(validator.validate(new PlatformAdminKeysOperationRequest())).hasSize(4);

        assertThat(validator.validate(KeyOwnerKeysOperationRequest.builder()
                .operation(KeysOperationType.REVOKED)
                .accounts(List.of())
                .issuerServices(List.of("s1"))
                .build())).hasSize(1);
    }

    @Test
    void statusRequests_requireStatus() {
        assertThat(validator.validate(new UpdateKeyStatusRequest())).hasSize(1);
        assertThat(validator.validate(new UpdateAuthorizationsRequest())).hasSize(1);
        assertThat(validator.validate(new UpdateOwnerStatusForAccountRequest())).hasSize(1);
        assertThat(validator.validate(
                new UpdateOwnerStatusForAccountAndIssuerServiceRequest())).hasSize(1);
    }

    @Test
    void keyOperationResponse_serializesProgress() throws Exception {
        UUID operationId = UUID.randomUUID();
        KeyOperationResponse response = KeyOperationResponse.builder()
                .operationId(operationId)
                .operation(KeysOperationType.REVOKED)
                .status(KeyOperationStatus.PENDING)
                .matchedCount(0L)
                .completedCount(0L)
                .failedCount(0L)
                .submittedAt(Instant.parse("2026-09-09T18:00:00Z"))
                .updatedAt(Instant.parse("2026-09-09T18:00:00Z"))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("operationId").asString()).isEqualTo(operationId.toString());
        assertThat(json.get("operation").asString()).isEqualTo("REVOKED");
        assertThat(json.get("status").asString()).isEqualTo("PENDING");
        assertThat(json.get("matchedCount").asLong()).isZero();
        assertThat(mapper.readValue(mapper.writeValueAsString(response),
                                    KeyOperationResponse.class)).isEqualTo(response);
    }
}
