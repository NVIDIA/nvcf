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
package com.nvidia.nvcf.service.ess;

import com.fasterxml.jackson.annotation.JsonProperty;
import com.google.common.annotations.VisibleForTesting;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import lombok.Builder;
import lombok.NonNull;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.service.annotation.DeleteExchange;
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.annotation.PutExchange;
import tools.jackson.databind.JsonNode;

public interface EssStubService {

    @Value
    @Jacksonized
    @Builder
    class SaveSecretsRequest {
        @NonNull
        Map<String, JsonNode> data;
    }

    @Value
    @Jacksonized
    @Builder
    class SaveSecretsResponse {
        @NonNull
        SaveSecretsData data;

        @Value
        @Jacksonized
        @Builder
        public static class SaveSecretsData {
            @JsonProperty("created_time")
            Instant createdTime;
            UUID version;
        }
    }

    @Value
    @Jacksonized
    @Builder
    @VisibleForTesting
    class FetchSecretsResponse {
        @NonNull
        FetchSecretData data;

        @Value
        @Jacksonized
        @Builder
        public static class FetchSecretData {
            @NonNull
            Map<String, JsonNode> data; // Object will be a Map when response is deserialized
        }
    }

    @PutExchange("/v1/functions/{functionId}/versions/{versionId}")
    SaveSecretsResponse saveFunctionVersionSecrets(
            @PathVariable UUID functionId,
            @PathVariable UUID versionId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace,
            @RequestBody SaveSecretsRequest payload);

    @PutExchange("/v1/accounts/{ncaId}/telemetries/{telemetryId}")
    SaveSecretsResponse saveTelemetrySecret(
            @PathVariable String ncaId,
            @PathVariable UUID telemetryId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace,
            @RequestBody SaveSecretsRequest payload);

    @PutExchange("/v1/accounts/{ncaId}/registry-credentials/{credentialId}")
    SaveSecretsResponse saveRegistryCredentialSecret(
            @PathVariable String ncaId,
            @PathVariable UUID credentialId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace,
            @RequestBody SaveSecretsRequest payload);

    @GetExchange("/v1/functions/{functionId}/versions/{versionId}")
    FetchSecretsResponse fetchFunctionVersionSecrets(
            @PathVariable UUID functionId,
            @PathVariable UUID versionId,
            @RequestParam("query_type") String queryType,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @GetExchange("/v1/accounts/{ncaId}/telemetries/{telemetryId}")
    FetchSecretsResponse fetchTelemetrySecret(
            @PathVariable String ncaId,
            @PathVariable UUID telemetryId,
            @RequestParam("query_type") String queryType,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @GetExchange("/v1/accounts/{ncaId}/registry-credentials/{credentialId}")
    FetchSecretsResponse fetchRegistryCredentialSecret(
            @PathVariable String ncaId,
            @PathVariable UUID credentialId,
            @RequestParam("query_type") String queryType,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @DeleteExchange("/v1/functions/{functionId}/versions/{versionId}")
    void deleteFunctionVersionSecrets(
            @PathVariable UUID functionId,
            @PathVariable UUID versionId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @DeleteExchange("/v1/functions/{functionId}")
    void deleteFunctionSecrets(
            @PathVariable UUID functionId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @DeleteExchange("/v1/accounts/{ncaId}/telemetries/{telemetryId}")
    void deleteTelemetrySecret(
            @PathVariable String ncaId,
            @PathVariable UUID telemetryId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);

    @DeleteExchange("/v1/accounts/{ncaId}/registry-credentials/{credentialId}")
    void deleteRegistryCredentialSecret(
            @PathVariable String ncaId,
            @PathVariable UUID credentialId,
            @RequestHeader("X-ESS-NAMESPACE") String namespace);
}
