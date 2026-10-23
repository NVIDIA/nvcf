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


package com.nvidia.apikeys.web;

/**
 * Paths and parameter names shared by the key-owner, account-admin and platform-admin
 * controllers. The actor prefix selects the actor, the remaining paths are the same.
 */
public final class KeyManagementRoutes {

    public static final String KEY_OWNER_PREFIX = "/v2/api-keys/key-owner";
    public static final String ACCOUNT_ADMIN_PREFIX = "/v2/api-keys/account-admin";
    public static final String PLATFORM_ADMIN_PREFIX = "/v2/api-keys/platform-admin";

    public static final String NCA_ID = "ncaId";
    public static final String SERVICE_ID = "serviceId";
    public static final String USER_ID = "userId";
    public static final String KEY_ID = "keyId";

    public static final String QUERY_NCA_ID = "nca-id";
    public static final String QUERY_SERVICE_ID = "service-id";
    public static final String QUERY_USER_ID = "user-id";
    public static final String QUERY_STATUS = "status";
    public static final String QUERY_LIMIT = "limit";
    public static final String QUERY_CURSOR = "cursor";

    public static final String SCOPED_KEYS =
            "/accounts/{ncaId}/services/{serviceId}/users/{userId}/keys";
    public static final String SCOPED_KEY = SCOPED_KEYS + "/{keyId}";
    public static final String SCOPED_KEY_STATUS = SCOPED_KEY + "/status";
    public static final String SCOPED_KEY_AUTHORIZATIONS = SCOPED_KEY + "/authorizations";
    public static final String ACCOUNT_KEYS = "/accounts/{ncaId}/keys";
    public static final String USER_KEYS = "/users/{userId}/keys";
    public static final String ALL_KEYS = "/keys";
    public static final String USER_STATUSES = "/users/{userId}/statuses";
    public static final String ACCOUNT_USER_STATUSES = "/accounts/{ncaId}/users/{userId}/statuses";
    public static final String ACCOUNT_USER_STATUS = "/accounts/{ncaId}/users/{userId}/status";
    public static final String SCOPED_USER_STATUS =
            "/accounts/{ncaId}/services/{serviceId}/users/{userId}/status";

    private KeyManagementRoutes() {
    }
}
