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
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.apikeys.utils.JsonUtils;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.Test;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

class KeyDtosJsonTest {

    private final JsonMapper mapper = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void createKeyRequest_deserializesCamelCaseBody() throws Exception {
        CreateKeyRequest request = mapper.readValue("""
                {
                  "description": "CLI key",
                  "expiresAt": "2027-01-01T00:00:00Z",
                  "audienceServiceIds": ["cloud-functions", "cloud-tasks"],
                  "authorizations": {"policies": []}
                }
                """, CreateKeyRequest.class);

        assertThat(request.getDescription()).isEqualTo("CLI key");
        assertThat(request.getExpiresAt()).isEqualTo(Instant.parse("2027-01-01T00:00:00Z"));
        assertThat(request.getAudienceServiceIds())
                .containsExactlyInAnyOrder("cloud-functions", "cloud-tasks");
        assertThat(request.getAuthorizations().get("policies").isArray()).isTrue();
    }

    @Test
    void createKeyRequest_leavesAudiencesNullWhenAbsent() throws Exception {
        CreateKeyRequest request = mapper.readValue("{\"description\":\"k\"}",
                                                    CreateKeyRequest.class);

        assertThat(request.getAudienceServiceIds()).isNull();
        assertThat(request.getExpiresAt()).isNull();
    }

    @Test
    void updateKeyStatusRequest_acceptsSettableStatuses() throws Exception {
        for (SettableKeyStatus status : SettableKeyStatus.values()) {
            UpdateKeyStatusRequest request = mapper.readValue(
                    "{\"status\":\"" + status + "\"}", UpdateKeyStatusRequest.class);

            assertThat(request.getStatus()).isEqualTo(status);
        }
    }

    @Test
    void updateKeyStatusRequest_rejectsExpired() {
        assertThatThrownBy(() -> mapper.readValue(
                "{\"status\":\"EXPIRED\"}", UpdateKeyStatusRequest.class))
                .isInstanceOf(Exception.class);
    }

    @Test
    void updateAuthorizationsRequest_deserializesPolicies() throws Exception {
        UpdateAuthorizationsRequest request = mapper.readValue(
                "{\"authorizations\":{\"policies\":[{\"aud\":\"cloud-functions\"}]}}",
                UpdateAuthorizationsRequest.class);

        assertThat(request.getAuthorizations().get("policies").get(0).get("aud").asString())
                .isEqualTo("cloud-functions");
    }

    @Test
    void keyResponse_serializesCamelCaseAndOmitsNulls() throws Exception {
        KeyResponse response = KeyResponse.builder()
                .id("8f751f65-bbb4-4e2f-843d-a828573eddf2")
                .value("nvcfapi-**********abc")
                .status(KeyStatusV2.REVOKED)
                .userId("user-1")
                .ncaId("account-a")
                .issuerServiceId("cloud-functions")
                .audienceServiceIds(Set.of("cloud-functions"))
                .description("CLI key")
                .createdAt(Instant.parse("2026-09-01T00:00:00Z"))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("id").asString()).isEqualTo("8f751f65-bbb4-4e2f-843d-a828573eddf2");
        assertThat(json.get("status").asString()).isEqualTo("REVOKED");
        assertThat(json.get("userId").asString()).isEqualTo("user-1");
        assertThat(json.get("ncaId").asString()).isEqualTo("account-a");
        assertThat(json.get("issuerServiceId").asString()).isEqualTo("cloud-functions");
        assertThat(json.has("ownerType")).isFalse();
        assertThat(json.has("owner_type")).isFalse();
        assertThat(json.has("authorizations")).isFalse();
        assertThat(json.has("lastUsedAt")).isFalse();
        assertThat(json.has("expiresAt")).isFalse();
    }

    @Test
    void listKeysResponse_omitsCursorOnLastPage() throws Exception {
        ListKeysResponse response = ListKeysResponse.builder()
                .keys(List.of(KeyResponse.builder().id("k1").build()))
                .limit(50)
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("keys").size()).isEqualTo(1);
        assertThat(json.get("limit").asInt()).isEqualTo(50);
        assertThat(json.has("cursor")).isFalse();
    }

    @Test
    void listKeysResponse_includesCursorWhenMorePages() throws Exception {
        ListKeysResponse response = ListKeysResponse.builder()
                .keys(List.of())
                .limit(1)
                .cursor("opaque-cursor")
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("cursor").asString()).isEqualTo("opaque-cursor");
    }

    @Test
    void keyResponse_toStringNeverExposesTheCompleteKey() {
        String secret = "nvcfapi-complete-key-value";
        KeyResponse.KeyResponseBuilder builder = KeyResponse.builder()
                .id("k1")
                .value(secret);
        KeyResponse response = builder.build();

        assertThat(response.toString()).contains("k1").doesNotContain(secret);
        assertThat(builder.toString()).doesNotContain(secret);
        assertThat(response.toBuilder().toString()).doesNotContain(secret);
        assertThat(ListKeysResponse.builder().keys(List.of(response)).build().toString())
                .doesNotContain(secret);
    }
}
