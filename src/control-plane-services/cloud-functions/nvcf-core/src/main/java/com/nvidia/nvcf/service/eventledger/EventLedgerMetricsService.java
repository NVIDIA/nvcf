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
package com.nvidia.nvcf.service.eventledger;

import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.UpstreamException;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.stereotype.Service;

@Service
public class EventLedgerMetricsService {

    private final Counter rejectedCounter;
    private final Counter publishFailureCounter;
    private final Counter clientErrorCounter;
    private final Counter serverErrorCounter;

    public EventLedgerMetricsService(MeterRegistry meterRegistry) {
        this.rejectedCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_rejected_total");
        this.publishFailureCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_publish_failures_total");
        this.clientErrorCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_client_errors_total");
        this.serverErrorCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_server_errors_total");
    }

    public void recordRejected() {
        rejectedCounter.increment();
    }

    public void recordEnqueueFailure() {
        publishFailureCounter.increment();
    }

    public void recordPublicationFailure(Exception ex) {
        if (ex instanceof UpstreamException upstream
                && upstream.getCause() instanceof BootResponseException response
                && response.getStatusCode().is4xxClientError()) {
            clientErrorCounter.increment();
        } else if (ex instanceof UpstreamException) {
            serverErrorCounter.increment();
        }
        publishFailureCounter.increment();
    }
}
