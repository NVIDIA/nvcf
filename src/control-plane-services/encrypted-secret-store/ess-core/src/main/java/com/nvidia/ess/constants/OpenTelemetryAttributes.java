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
package com.nvidia.ess.constants;

import lombok.experimental.UtilityClass;

@UtilityClass
public class OpenTelemetryAttributes {

    public static final String REQUEST_ID_KEY = "ess.request_id";
    public static final String AGENT_ID_KEY = "ess.agent_id";
    public static final String MAX_RETRY_ATTEMPTS_KEY = "ess.retries.max_retries";
    public static final String MIN_RETRY_BACKOFF_TIME_MILLIS_KEY = "ess.retries.min_backoff_millis";
    public static final String MAX_RETRY_BACKOFF_TIME_MILLIS_KEY = "ess.retries.max_backoff_millis";
    public static final String URL_FULL_KEY = "url.full";
    public static final String PARTIAL_DELETE_TYPE_KEY = "ess.partial_delete_type";
    public static final String PARTIAL_CREATE_TYPE_KEY = "ess.partial_create_type";
    public static final String LB_TRACE_ID_KEY = "ess.lb_trace_id";
    public static final String NAMESPACE_KEY = "ess.namespace";
    public static final String ENTITY_TYPE_KEY = "ess.entity_type";
    public static final String RETRY_NUM_KEY = "ess.retry_num";
    public static final String EXHAUSTED_RETRIES_KEY = "ess.exhausted_retries";
    public static final String LWT_WRITE_FAILURE_OPERATION_KEY = "ess.lwt_failure_operation";
    public static final String SECRET_READ_AUTH_TYPE_KEY = "ess.secret_read_auth_type";
    public static final String SECRET_QUERY_TYPE_KEY = "ess.secret.query_type";
    public static final String SECRET_CAS_ERROR_PROVIDED_VERSION_KEY = "ess.secret.cas_error.provided_version";
    public static final String SECRET_CAS_ERROR_ACTUAL_VERSION_KEY = "ess.secret.cas_error.actual_version";


    public enum PartialDeleteType {
        SECRET_VERSION_ON_ENTITY,
        SECRET_PATH_ON_ENTITY,
        ENTITY_ON_ENTITY,
        SECRET_PATH_ON_SECRET,
        SECRET_PATH_CAS_ON_SECRET
    }


    public enum PartialCreateType {
        SECRET_VERSION_AFTER_PATH_BATCH,
        SECRET_VERSION_AFTER_PATH_UNKNOWN
    }
}
