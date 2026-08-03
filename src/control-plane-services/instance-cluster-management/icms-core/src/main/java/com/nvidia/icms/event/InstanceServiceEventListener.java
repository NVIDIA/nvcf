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
package com.nvidia.icms.event;

import static com.nvidia.icms.service.telemetry.model.Events.NCA_ID_ACCOUNT_NAME_DETAILS;

import com.nvidia.icms.outbound.ngc.NgcRequestHandler;
import com.nvidia.icms.outbound.ngc.model.NgcNcaIdInfoResponse;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.context.event.EventListener;
import org.springframework.scheduling.annotation.Async;
import org.springframework.stereotype.Service;

/**
 * Service for handling asynchronous event processing in ICMS.
 * <p>
 * This event listener is responsible for processing various events in the system
 * asynchronously to avoid blocking the main request flow. It handles operations
 * such as updating account names, which can be performed after returning a response
 * to the client.
 * </p>
 * <p>
 * The listener uses Spring's event mechanism with {@link EventListener} and
 * {@link Async} annotations to handle events in a non-blocking manner.
 * </p>
 */
@Service
@Slf4j
@AllArgsConstructor
public class InstanceServiceEventListener {

    private final NgcRequestHandler ngcRequestHandler;
    private final TelemetryEventClient telemetryEventClient;

    /**
     * Asynchronously retrieves the account name for a NCA ID and records it in telemetry.
     * <p>
     * This event listener is triggered when a {@link NcaIdAccountNameEvent} is published.
     * It retrieves the account name using the NCA ID from the NGC service and sends the
     * information as a telemetry event. The operation is performed asynchronously to
     * allow the main request flow to continue without waiting for this process.
     * </p>
     * <p>
     * If an exception occurs during the process, it is logged and an error telemetry
     * event is triggered, but the exception is not propagated to avoid affecting the
     * main application flow.
     * </p>
     *
     * @param event The {@link NcaIdAccountNameEvent} containing the ncaId and requestId
     */
    @Async("taskExecutor")
    @EventListener
    public void sendNcaIdAccountNameEventAsync(@NotNull NcaIdAccountNameEvent event) {

        String ncaId = event.getNcaId();
        String requestId = event.getRequestId();

        try {
            // Fetching accountName
            NgcNcaIdInfoResponse ngcNcaIdInfoResponse = ngcRequestHandler.getNcaIdDetails(ncaId);
            if (!isResponseValid(ngcNcaIdInfoResponse)) {
                log.info(
                        "function: sendNcaIdAccountNameEventAsync, ncaId: {}, response from NGC is invalid, response: {} ",
                        ncaId, ngcNcaIdInfoResponse);
                return;
            }

            // Sending telemetry event
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(
                                                                       NCA_ID_ACCOUNT_NAME_DETAILS.toString())
                                                               .withNcaIdPartnerName(ngcNcaIdInfoResponse.getAccountName())
                                                               .withNcaIdAccountType(ngcNcaIdInfoResponse.getAccountType())
                                                               .withNcaId(ncaId)
                                                               .withRequestId(requestId)));

            log.info("Event {}: ncaIdAccountName: {} accountType: {} ncaId: {} requestId: {}",
                     NCA_ID_ACCOUNT_NAME_DETAILS, ngcNcaIdInfoResponse.getAccountName(),
                     ngcNcaIdInfoResponse.getAccountType(), ncaId, requestId);

        } catch (Exception exception) {
            // Suppressing the exception
            log.error(
                    "InstanceServiceEventListener: function: sendNcaIdAccountNameEventAsync, error: {}, exception: ",
                    exception.getMessage(), exception);

            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(Events.NCA_ID_ACCOUNT_NAME_UPDATE_ASYNC_EVENT_FAILED.toString())
                                                               .withRequestId(requestId)
                                                               .withNcaId(ncaId)
                                                               .withError(exception.getMessage())));
        }
    }


    private boolean isResponseValid(@Nullable NgcNcaIdInfoResponse ngcNcaIdInfoResponse) {

        if (ngcNcaIdInfoResponse == null) {
            return false;
        }

        // If least one field from response is non-null then we will consider it as valid response and send event
        return StringUtils.isNotBlank(ngcNcaIdInfoResponse.getAccountName())
                || ngcNcaIdInfoResponse.getAccountType() != null;
    }
}
