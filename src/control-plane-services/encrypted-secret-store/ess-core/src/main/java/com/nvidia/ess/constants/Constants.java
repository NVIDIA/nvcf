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

public final class Constants {

    // Prevent explicit instantiation as all variables are static
    private Constants() {
    }

    public static final String[] OPEN_ENDPOINTS = {
            "/v3/openapi/**",
            "/v3/api-docs/**",
            "/v3/api-docs.yaml",
            "/swagger-ui/**",
            "/health/**",
            "/info",
            "/swagger-ui.html",
            "/webjars/swagger-ui/**",
            "/v1/**"
    };

    public static String[] getOpenEndpoints() {
        return OPEN_ENDPOINTS;
    }

    public static final class RetriesExhaustedErrorTags {

        // Prevent explicit instantiation as all variables are static
        private RetriesExhaustedErrorTags() {}

        public static final class Errors {

            // Prevent explicit instantiation as all variables are static
            private Errors() {}

            public static final String TOO_MANY_REQUESTS = "TOO_MANY_REQUESTS";
            public static final String INTERNAL_SERVER_ERROR = "INTERNAL_SERVER_ERROR";
        }

        public static final class SubErrors {

            // Prevent explicit instantiation as all variables are static
            private SubErrors() {}

            public static final String TOO_MANY_REQUESTS_ON_ENTITY = "TOO_MANY_REQUESTS_ON_ENTITY";
            public static final String TOO_MANY_REQUESTS_ON_SECRET = "TOO_MANY_REQUESTS_ON_SECRET";
            public static final String UNHANDLED_UPSTREAM_ERROR = "UNHANDLED_UPSTREAM_ERROR";
            public static final String SECRET_VERSION_WRITE_FAILURE = "SECRET_VERSION_WRITE_FAILURE";
            public static final String ENTITY_VERSION_PREFETCH_FAILURE = "ENTITY_VERSION_PREFETCH_FAILURE";
            public static final String EXISTING_PATH_FETCH_FAILURE = "EXISTING_PATH_FETCH_FAILURE";
            public static final String UNHANDLED_PATH_DELETION_FAILURE = "UNHANDLED_PATH_DELETION_FAILURE";
            public static final String SECRET_VERSION_EXISTENCE_CHECK_FAILURE = "SECRET_VERSION_EXISTENCE_CHECK_FAILURE";
            public static final String SECRET_VERSION_FETCH_FAILURE = "SECRET_VERSION_FETCH_FAILURE";
            public static final String GEN_SECRET_VERSION_TOO_OLD = "GEN_SECRET_VERSION_TOO_OLD";
            public static final String EMPTY_NON_LWT_SECRET_VERSION_WRITE = "EMPTY_NON_LWT_SECRET_VERSION_WRITE";
            public static final String SECRET_PATH_DB_WRITE_FAILURE = "SECRET_PATH_DB_WRITE_FAILURE";
        }
    }

    public static final String X_ESS_TOKEN_HEADER = "X-ESS-TOKEN";
    public static final String X_ESS_NAMESPACE_HEADER = "X-ESS-NAMESPACE";
    public static final String X_ESS_REQUEST_ID_HEADER = "X-ESS-Request-Id";
    public static final String X_ESS_AGENT_ID_HEADER = "X-ESS-Agent-Id";
    public static final String X_AMZN_TRACE_ID = "X-Amzn-Trace-Id";
    public static final String MDC_REQUEST_ID_KEY = "request_id";
    public static final String MDC_AGENT_ID_KEY = "agent_id";
    public static final String MDC_LB_TRACE_ID_KEY = "lb_trace_id";
    public static final String UNAUTHORIZED = "unauthorized operation";

    public static final String JWKS_URI = "/.well-known/jwks.json";

    public static final String UNKNOWN_NAMESPACE = "UNKNOWN";
    public static final String REDACTED = "REDACTED";

    public static final String PATTERN_ALLOW_ALL = "^[\\s\\S]*$";

    public static final String MSG_INTERNAL_ERROR = "Internal Error";
    public static final String MSG_TOO_MANY_REQUESTS = "Too many requests";
    public static final String SERVER_EXCHANGE_REJECTED = "Request rejected by server due to malformed or illegal " +
            "header or other attribute";

    public static final String MSG_ILLEGAL_URI = "Illegal URI detected";
    public static final String MSG_SECRET_VERSION_NOT_MOST_RECENT = "Secret at (namespace='%s', entity='%s', " +
            "secretPath='%s') does not have a most recent version ID with value '%s', the most recent version is '%s'";
    public static final String MSG_SECRET_VERSION_NOT_MOST_RECENT_AT_WRITE_TIME = "Secret at (namespace='%s',"
            + " entity='%s', secretPath='%s') has a stored payload with version=%s but it is not the most recent version.";
    public static final String MSG_SECRET_VERSION_NOT_PRESENT = "Secret at (namespace='%s', entity='%s', " +
            "secretPath='%s') does not have version=%s as the most recent version.";
    public static final String MSG_SECRET_VERSION_EMPTY_WRITE = "secret-version write operation finished but did not " +
            "write any rows: (namespace='%s', entity='%s', secretPath='%s').";
    public static final String MSG_SECRET_NOT_FOUND = "No match found for secret with path: '%s' under namespace: '%s', entity: '%s'";
    public static final String MSG_SECRET_VERSION_NOT_FOUND = "No match found for secret-version: %s, for secret with path: '%s' under namespace: '%s', entity: '%s'";
    public static final String MSG_NAMESPACE_NOT_FOUND = "namespace '%s': not found";
    public static final String MSG_ENTITY_TYPE_NOT_FOUND = "entity type '%s': not found";
    public static final String MSG_ENTITY_NOT_FOUND = "entity '%s/%s': not found";
    public static final String MSG_FAILED_EXCEPTION_CREATION = "failed to instantiate exception '{}'";
    public static final String MSG_CLIENT_ID_NOT_REGISTERED = "client id=%s is not registered";
    public static final String MSG_CLIENT_ID_ALREADY_REGISTERED = "client id=%s is already registered. If you want to register please delete it and re-register";
    public static final String MSG_CAN_NOT_REMOVE_SELF = "can not remove self";
    public static final String MSG_TOKEN_EXPIRED = "JWT expired at %s";
    public static final String MSG_INSUFFICIENT_SCOPES = "insufficient scope permissions, requires one of the scopes: %s";
    public static final String MSG_INVALID_ASSERTIONS = "insufficient permissions, passed assertion in invalid format: %s";
    public static final String MSG_FAILED_TO_MATCH_CLAIM = "insufficient permission to access %s=%s, passed claim %s=%s";
    public static final String MSG_MISSING_IAT = "missing iat field in the Jwt";
    public static final String MSG_ONLY_FETCH_SECRET_ALLOWED = "only fetch operations are allowed for notary token";
    public static final String MSG_NO_AUTH_HEADER = "no authorization header provided";
    public static final String MSG_FAILED_TO_LOOKUP_AUTH = "failed to look up auth information";
    public static final String MSG_MISSING_AUTHORIZATION = "missing authorization";
    public static final String MSG_OPERATION_ALLOWED_BY_OPERATOR_ONLY = "operation only allowed by ess operators";
    public static final String MSG_OPERATION_NOT_ALLOWED_BY_OPERATOR = "operation not allowed by ess operators";
    public static final String MSG_NAMESPACE_BEING_DELETED = "Namespace %s is being deleted in the background. Please wait until the background process completes.";
    public static final String MSG_NAMESPACE_EXISTS = "Namespace %s already exists.";
}
