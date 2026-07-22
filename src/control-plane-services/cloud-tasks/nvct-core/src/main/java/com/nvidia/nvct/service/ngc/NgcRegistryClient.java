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
package com.nvidia.nvct.service.ngc;

import tools.jackson.databind.JsonNode;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.service.ngc.dto.CreateModelRequest;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils;
import com.nvidia.nvct.util.NvctOAuth2ClientUtils.ManagedHttpResources;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Service
@RefreshScope
@Slf4j
public class NgcRegistryClient {
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    private static final String MESG_INVALID_RESULTS_LOCATION =
            "Invalid request: resultsLocation '%s' should be either in 'orgName/modelName' or " +
                    "'orgName/teamName/modelName' format";
    private static final int NGC_MODEL_NAME_MAX_LENGTH = 64;
    private static final String MESG_MODEL_NAME_TOO_LONG =
            "Invalid request: model name '%s' in resultsLocation exceeds the maximum allowed " +
                    "length of " + NGC_MODEL_NAME_MAX_LENGTH + " characters";
    private static final String MESG_CREATED_AND_DELETED_MODEL =
            "Successfully created and deleted model '{}' using the NGC_API_KEY";
    private static final String MESG_REGISTRY_RESPONSE_STATUS =
            "Cannot create results/checkpoints using the specified secret NGC_API_KEY in the " +
                    "org/team specified in the 'resultsLocation' - NGC Registry response " +
                    "status code '%d' ";

    public static final String CLIENT_REGISTRATION_ID = "ngc-registry";
    private static final String BEARER_TOKEN = "Bearer %s";

    private final NgcRegistryStubService ngcRegistryStubService;

    private record ModelDetails(String orgName, String teamName, String modelName) { }

    public NgcRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            ManagedHttpResources ngcRegistryHttpResources,
            @Value("${nvct.registries.recognized.model.ngc.hostname}") String hostname) {
        var baseUrl = hostname.toLowerCase().startsWith("http")
                            ? hostname : "https://" + hostname; // For tests.
        var webClient = webClientBuilder
                .baseUrl(baseUrl.replace("-ngc", ""))
                .clientConnector(ngcRegistryHttpResources.connector())
                .filter(NvctOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(NvctOAuth2ClientUtils.getResponseFilterProcessor("NGC"))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.ngcRegistryStubService = factory.createClient(NgcRegistryStubService.class);
    }

    // Validate the NGC_API_KEY by creating the model and then deleting the model specified in
    // the resultsLocation under the specified org / team.
    public void validate(JsonNode ngcApiKey, String resultsLocation) {
        var value = ngcApiKey.isString() ? ngcApiKey.asString() : ngcApiKey.toString();
        if (StringUtils.isBlank(value)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ngcApiKey");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(resultsLocation)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("resultsLocation");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var modelDetails = getModelDetails(resultsLocation);
        var payload = CreateModelRequest.builder()
                .name(modelDetails.modelName())
                .displayName(modelDetails.modelName())
                .precision(StringUtils.EMPTY)
                .framework(StringUtils.EMPTY)
                .build();
        var bearerToken = BEARER_TOKEN.formatted(value);
        if (StringUtils.isBlank(modelDetails.teamName())) {
            ngcRegistryStubService.createOrgModel(bearerToken,
                                                  modelDetails.orgName(),
                                                  payload);
            ngcRegistryStubService.deleteOrgModel(bearerToken, modelDetails.orgName(),
                                                  modelDetails.modelName());
        } else {
            ngcRegistryStubService.createTeamModel(bearerToken,
                                                   modelDetails.orgName(),
                                                   modelDetails.teamName(),
                                                   payload);
            ngcRegistryStubService.deleteTeamModel(bearerToken, modelDetails.orgName(),
                                                   modelDetails.teamName(),
                                                   modelDetails.modelName());
        }
        log.info(MESG_CREATED_AND_DELETED_MODEL, modelDetails.modelName());
    }

    private static ModelDetails getModelDetails(String resultsLocation) {
        int slashCount = StringUtils.countMatches(resultsLocation, '/');
        if ((slashCount == 0) || (slashCount > 2)) {
            var mesg = MESG_INVALID_RESULTS_LOCATION.formatted(resultsLocation);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var orgName = resultsLocation.substring(0, resultsLocation.indexOf('/'));
        String teamName = null;
        var modelName = "";

        if (slashCount == 1) {
            modelName = resultsLocation.substring(orgName.length() + 1);
        } else {
            var index = resultsLocation.lastIndexOf('/');
            teamName = resultsLocation.substring(orgName.length() + 1, index);
            modelName = resultsLocation.substring(index + 1);
        }

        // Validate that model name doesn't exceed NGC's maximum limit
        if (modelName.length() > NGC_MODEL_NAME_MAX_LENGTH) {
            var mesg = MESG_MODEL_NAME_TOO_LONG.formatted(modelName);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // Model name length can only be 64 characters. Since we are appending a full UUID,
        // truncate the model name if needed to ensure the final name fits within the limit.
        var uuid = UUID.randomUUID().toString();
        // Reserve space for dash and UUID
        var maxModelNameLength = NGC_MODEL_NAME_MAX_LENGTH - uuid.length() - 1;
        if (modelName.length() > maxModelNameLength) {
            modelName = modelName.substring(0, maxModelNameLength);
        }

        // Create a unique model name so that there is no conflict with existing ones when
        // we try to create a new model to validate the NGC_API_KEY. This newly created model
        // will be deleted immediately.
        return new ModelDetails(orgName, teamName, modelName + "-" + uuid);
    }
}
