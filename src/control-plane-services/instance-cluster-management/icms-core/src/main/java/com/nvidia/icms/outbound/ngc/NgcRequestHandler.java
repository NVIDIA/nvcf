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
package com.nvidia.icms.outbound.ngc;

import com.nvidia.icms.configuration.ngc.NgcConfigurationProperties;
import com.nvidia.icms.outbound.ngc.model.AccountType;
import com.nvidia.icms.outbound.ngc.model.GetOrganizationResponse;
import com.nvidia.icms.outbound.ngc.model.NgcNcaIdInfoResponse;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClientResponseException;

@Slf4j
@AllArgsConstructor
@Service
public class NgcRequestHandler {

    private final NgcClient ngcClient;
    private final TelemetryEventClient telemetryEventClient;
    private final NgcConfigurationProperties ngcConfigurationProperties;

    @Observed
    public @Nullable NgcNcaIdInfoResponse getNcaIdDetails(@NotNull String ncaId) {

        try {
            if (!ngcConfigurationProperties.isEnabled()) {
                log.info("NGC_SERVICE: NGC service communication is not enabled, ncaId: {} displayName: null",
                        ncaId);
                return null;
            }

            GetOrganizationResponse response = ngcClient.getOrgInfo(ncaId);
            return extractResponse(response, ncaId);

        } catch (WebClientResponseException ex) {
            // Suppressing the error
            String errMsg = String.format(
                    "Failed to invoke NGC service for orgInfo, HTTP %d, error: %s",
                    ex.getStatusCode().value(), ex.getMessage());
            logErrorMessage(errMsg, ex);
            sendFailureEvent(errMsg, ncaId, ex.getStatusCode().value());

        } catch (Exception exception) {
            // Suppressing the error
            String errMsg = String.format("Failed to invoke NGC service for orgInfo, error: %s", exception.getMessage());
            logErrorMessage(errMsg, exception);
            sendFailureEvent(errMsg, ncaId, 500);
        }

        return null;
    }

    private @Nullable NgcNcaIdInfoResponse extractResponse(
            @Nullable GetOrganizationResponse responseBody,
            @NotNull String ncaId) {
        if (responseBody == null) {
            String errMsg = "Failed to extract NCA ID information, responseBody is null";
            sendFailureEvent(errMsg, ncaId, 500);
            logErrorMessage(errMsg);
            return null;
        }

        if (responseBody.getOrganization() == null) {
            String errMsg = "Failed to extract NCA ID information, getOrganization is null";
            sendFailureEvent(errMsg, ncaId, 500);
            logErrorMessage(errMsg);
            return null;
        }

        return NgcNcaIdInfoResponse.builder()
                .accountType(getAccountType(responseBody))
                .accountName(getAccountName(responseBody, ncaId))
                .build();
    }

    private @NotNull AccountType getAccountType(@NotNull GetOrganizationResponse responseBody) {

        String idpIdOfAccount = responseBody.getOrganization().getIdpId();
        String nvidiaInternalIdpId = ngcConfigurationProperties.getNvidiaInternalIdpId();
        // If IDP ID matches with nvidiaInternalIdpId then it is INTERNAL account
        if(StringUtils.isNotBlank(idpIdOfAccount) &&
                StringUtils.isNotBlank(nvidiaInternalIdpId) &&
                nvidiaInternalIdpId.equals(idpIdOfAccount)){
            return AccountType.INTERNAL;
        }

        /*
        1. If idpId is null then it is external account
        2. If idpId is non-null but doesn't match with NV IDP ID then it is external
         */
        return AccountType.EXTERNAL;
    }

    // DisplayName from NGC response is accountName associated with ncaId
    private @Nullable String getAccountName(@NotNull GetOrganizationResponse responseBody,
                                            @NotNull String ncaId) {
        String accountName = responseBody.getOrganization().getDisplayName();
        if (StringUtils.isBlank(accountName)) {
            String errMsg = "displayName is empty in response";
            sendFailureEvent(errMsg, ncaId, 500);
            logErrorMessage(errMsg);
            return null;
        }

        return accountName;
    }

    private void logErrorMessage(String errMsg, Exception exception) {
        log.error("Event: {} Error: {}, exception: ", Events.NGC_INVOCATION_FAILED, errMsg,
                  exception);
    }

    private void logErrorMessage(String errMsg) {
        log.error("Event: {} Error: {}", Events.NGC_INVOCATION_FAILED, errMsg);
    }

    private void sendFailureEvent(String errorMsg, String ncaId, Integer httpCode) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withEventName(Events.NGC_INVOCATION_FAILED.toString())
                                                           .withNcaId(ncaId)
                                                           .withHttpCode(httpCode)
                                                           .withError(errorMsg)));
    }
}
