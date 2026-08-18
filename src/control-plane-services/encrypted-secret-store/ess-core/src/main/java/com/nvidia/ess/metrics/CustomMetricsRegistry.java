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
package com.nvidia.ess.metrics;

import com.nvidia.ess.constants.AuthorizationType;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.DistributionSummary;
import io.micrometer.core.instrument.MeterRegistry;
import lombok.Setter;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;
import org.springframework.util.unit.DataSize;

/**
 * [WIP] Observability flow could be the following for error state metrics: 1. A certain "error"
 * metric goes past allowable threshold (alert or not) 2. In logs, a corresponding message regex
 * should be usable to correlate to the metric 3. In tracing. a corresponding span attribute should
 * be usable to correlate to the traces
 * <p></p>
 *
 * Ideally, there should be a table in a runbook or some centralized document that allows to go from
 * any of the 3 signals (metric, log, span/trace) to figure out how to observe it in the other system
 */
@Component
public class CustomMetricsRegistry {

    @Setter(onMethod_ = {@Autowired})
    private MeterRegistry meterRegistry;

    public static final String NAMESPACE_TAG = "ess_namespace";
    public static final String QUERY_TYPE_TAG = "query_type";

    private static final String REASON_TAG = "reason";
    private static final String OPERATION_TAG = "operation";
    private static final String AUTH_TYPE_TAG = "auth_type";
    private static final String SECRET_OPERATION_SUCCESS_TAG = "success";

    public void recordSecretRead(String namespace, AuthorizationType authorizationType, boolean isSuccessful) {
        Counter.builder("secret_reads")
                .tag(NAMESPACE_TAG, namespace)
                .tag(AUTH_TYPE_TAG, authorizationType.name())
                .tag(SECRET_OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of successful secret reads (per namespace and auth flow)")
                .register(meterRegistry)
                .increment();
    }


    public void recordSecretVersionsList(String namespace, boolean isSuccessful) {
        Counter.builder("secret_versions_lists")
                .tag(NAMESPACE_TAG, namespace)
                .tag(SECRET_OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of successful secret version lists (per namespace)")
                .register(meterRegistry)
                .increment();
    }


    public void recordSecretPathsList(String namespace, boolean isSuccessful) {
        Counter.builder("secret_paths_lists")
                .tag(NAMESPACE_TAG, namespace)
                .tag(SECRET_OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of successful secret path lists (per namespace)")
                .register(meterRegistry)
                .increment();
    }


    public void recordSecretCreate(String namespace, boolean isSuccessful) {
        Counter.builder("secret_creates")
                .tag(NAMESPACE_TAG, namespace)
                .tag(SECRET_OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of successful secret creates (per namespace)")
                .register(meterRegistry)
                .increment();
    }


    public void recordSecretDelete(String namespace, boolean isSuccessful) {
        Counter.builder("secret_deletes")
                .tag(NAMESPACE_TAG, namespace)
                .tag(SECRET_OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of successful secret deletes (per namespace)")
                .register(meterRegistry)
                .increment();
    }


    public void recordSecretPayloadSize(String namespace, int byteSize) {
        DistributionSummary.builder("secret_payload_size")
                .tag(NAMESPACE_TAG, namespace)
                .description("payload size of created secrets (per namespace)")
                // not sure about these thresholds yet, but we have to define them to get histograms
                .serviceLevelObjectives(
                        DataSize.ofBytes(64).toBytes(),
                        DataSize.ofBytes(128).toBytes(),
                        DataSize.ofBytes(256).toBytes(),
                        DataSize.ofBytes(512).toBytes(),
                        DataSize.ofKilobytes(1).toBytes(),
                        DataSize.ofKilobytes(2).toBytes(),
                        DataSize.ofKilobytes(4).toBytes(),
                        DataSize.ofKilobytes(8).toBytes(),
                        DataSize.ofKilobytes(16).toBytes(),
                        // avoid any issues with exact 32KB size bucketing, set to a lower threshold
                        DataSize.ofKilobytes(31).toBytes()
                )
                .baseUnit("bytes")
                .register(meterRegistry)
                .record(byteSize);
    }

    public void recordRetryableError(String namespace, String reason) {
        Counter.builder("all_retryable_errors")
                .tag(NAMESPACE_TAG, namespace)
                .tag(REASON_TAG, reason)
                .description(
                        "count of expected errors that should result in a retry (per namespace)")
                .register(meterRegistry)
                .increment();   
    }

    public void recordExhaustedRetryableError(String namespace, String reason) {
        Counter.builder("exhausted_retryable_errors")
                .tag(NAMESPACE_TAG, namespace)
                .tag(REASON_TAG, reason)
                .description(
                        "count of expected errors that exhausted all retries attempts (per namespace)")
                .register(meterRegistry)
                .increment();
    }

    public void recordPartialEntityDeletionOnPath(String namespace) {
        Counter.builder("entity_partial_deletes")
                .tag(REASON_TAG, "FAILED_PATH_DELETION")
                .tag(NAMESPACE_TAG, namespace)
                .description(
                        "count of partially successful entity deletions (per namespace and failure reason)")
                .register(meterRegistry)
                .increment();
    }


    public void recordPartialEntityDeletionOnEntity(String namespace) {
        Counter.builder("entity_partial_deletes")
                .tag(REASON_TAG, "FAILED_ENTITY_DELETION")
                .tag(NAMESPACE_TAG, namespace)
                .description(
                        "count of partially successful entity deletions (per namespace and failure reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordPartialSecretDeletionOnPath(String namespace) {
        Counter.builder("secret_partial_deletes")
                .tag(REASON_TAG, "FAILED_PATH_DELETION")
                .tag(NAMESPACE_TAG, namespace)
                .description(
                        "count of partially successful secret deletions (per namespace and failure reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordRetryablePartialSecretCreationOnVersion(String namespace) {
        Counter.builder("secret_partial_creates")
                .tag(REASON_TAG, "FAILED_VERSION_CREATION_RETRYABLE")
                .tag(NAMESPACE_TAG, namespace)
                .description(
                        "count of partially successful secret create/update operations " +
                        "(per namespace and failure reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordNonRetryableLwtFailure(String namespace, LwtOperation operation) {
        Counter.builder("lwt_failures")
                .tag(NAMESPACE_TAG, namespace)
                .tag(OPERATION_TAG, operation.name())
                .description("count of non-retryable LWT failures (per namespace and operation)")
                .register(meterRegistry)
                .increment();
    }

    public void recordRetryableLwtFailure(String namespace, LwtOperation operation) {
        Counter.builder("lwt_retryable_failures")
                .tag(NAMESPACE_TAG, namespace)
                .tag(OPERATION_TAG, operation.name())
                .description(
                        "count of retryable LWT failures that might or might not have resolved on retry (per namespace and operation)")
                .register(meterRegistry)
                .increment();
    }

    public void recordSecretCreateCasError(String namespace) {
        Counter.builder("secret_create_cas_errors")
                .tag(NAMESPACE_TAG, namespace)
                .description(
                        "count of CAS errors during secret creation when CAS version is specified (per namespace)")
                .register(meterRegistry)
                .increment();
    }

    public enum LwtOperation {
        SECRET_CREATION,
        PATH_CREATION,
        PATH_DELETION
    }
}
