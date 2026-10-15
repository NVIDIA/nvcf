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

package com.nvidia.apikeys;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import com.nvidia.apikeys.dto.keys.CreateKeyRequest;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.KeyLookupRequest;
import com.nvidia.apikeys.dto.keys.KeyOwnerDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.dto.keys.UpdateKeyOwnerStatusRequest;
import com.nvidia.apikeys.dto.keys.UpdateKeyStatusRequest;
import com.nvidia.apikeys.dto.services.ServiceDto;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.CachedIntrospectionResponse;
import com.nvidia.apikeys.vo.CreateKeyRequestVo;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.apikeys.vo.SuspendKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Set;

public class TestData {

    public static final String DATA_DOMAIN_KEY = "KVyrsdeNCW5IXxFgysCLN35sir4Uqh4ZuWUZmv9pBHRdIcUOTZ79JfMLDZlKEvPhKrVHZX-ZP1jpGMrxsKjfEXIiY_APV0dn-fp0mQBvC-GwbAot_w7ztxxXGrYX2vVcC5eGqpTR1x3up_OZHkMy6bfF731Qn_kZzOWOMNBWfBaU4_l2wbkolg";

    public static final JsonMapper TEST_OM = JsonUtils.getRequestResponseJsonMapper();

    public static final String SERVICE_ID_1 = "nvidia-cloud-functions-ncp-service-id-aketm";
    public static final String SERVICE_NAME_1 = "test-service";

    public static final Integer SERVICE_MAX_API_KEYS_PER_USER_1 = 10;
    public static final Integer SERVICE_MAX_API_KEY_TTL_DAYS_1 = 1;
    public static final Integer SERVICE_MAX_AUTHZ_SIZE_CHARS_1 = 1024;
    public static final Integer SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1 = 10;

    public static final Integer SERVICE_MAX_API_KEYS_PER_USER_2 = 2;
    public static final Integer SERVICE_MAX_API_KEY_TTL_DAYS_2 = 36524;
    public static final Integer SERVICE_MAX_AUTHZ_SIZE_CHARS_2 = 2048;
    public static final Integer SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_2 = 5;

    public static ServiceVo SERVICE_VO_1 = ServiceVo.builder()
            .serviceId(SERVICE_ID_1)
            .serviceName(SERVICE_NAME_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .maxApiKeysPerUser(SERVICE_MAX_API_KEYS_PER_USER_1)
            .maxApiKeyTtlDays(SERVICE_MAX_API_KEY_TTL_DAYS_1)
            .maxAuthzSizeChars(SERVICE_MAX_AUTHZ_SIZE_CHARS_1)
            .minAuthzUpdateIntervalSeconds(SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1)
            .build();

    public static ServiceDto SERVICE_DTO_1 = ServiceDto.builder()
            .serviceId(SERVICE_ID_1)
            .serviceName(SERVICE_NAME_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .maxApiKeysPerUser(SERVICE_MAX_API_KEYS_PER_USER_1)
            .maxApiKeyTtlDays(SERVICE_MAX_API_KEY_TTL_DAYS_1)
            .maxAuthzSizeChars(SERVICE_MAX_AUTHZ_SIZE_CHARS_1)
            .minAuthzUpdateIntervalSeconds(SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1)
            .build();

    // this key derived from external id above in mock OIDC service. do not change
    public static final String USER_KEY_OWNER_ID_1 = "yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs";

    public static final String SERVICE_ADMIN_USER_ID = "service-admin-user-id";



    public static final String TEST_TIME_STRING = "2023-10-03T08:25:24.00Z";
    public static final String KEY_EXPIRES_AT_1_STRING = "2023-10-04T08:25:24.00Z";
    public static final String KEY_EXPIRES_AT_2_STRING = "2023-10-04T07:25:24.00Z";
    public static final Instant TEST_TIME = Instant.parse(TEST_TIME_STRING);

    public static final KeyOwnerVo KEY_OWNER_VO_1 = KeyOwnerVo.builder()
            .ownerId(USER_KEY_OWNER_ID_1)
            .ownerStatus(KeyOwnerStatus.ACTIVE)
            .ownerType(KeyOwnerType.USER)
            .ownerStatusUpdatedAt(TEST_TIME)
            .build();

    public static final String API_KEY_1 = "nvcfapi-test-4QrM4DmXVfuFDvzqSUgC91eKw4S9cKqgMcNkbeMbKOwQ82nHmgIZ9Fm3DGgLxMMZ";
    public static final String API_KEY_2 = "nvcfapi-test-4QrM4DmXVfuFDvzqSUgC91eKw4S9cKqgMcNkbeMbKOwQ82nHmgIZ9Fm3DGgLx222";
    public static final String API_KEY_HASH_1 = "Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos";
    public static final String KEY_ID_1 = "123e4567-e89b-42d3-a456-556642440123";
    public static final String KEY_ID_2 = "123e4567-e89b-42d3-a456-556642440222";
    public static final String API_KEY_SUFFIX_1 = "test-key-**********123";
    public static final String KEY_DESCRIPTION_1 = "123e4567-description";
    public static final String KEY_DESCRIPTION_2 = "22222222-description";
    public static final String KEY_AUTHZ_1 = "{\"allow\":true}";
    public static final String KEY_AUTHZ_2 = "{\"allow\":true,\"other_id\":\"other-id-value-1\"}";
    public static final Instant KEY_EXPIRES_AT_1 = Instant.parse(KEY_EXPIRES_AT_1_STRING);
    public static final Instant KEY_EXPIRES_AT_2 = Instant.parse(KEY_EXPIRES_AT_2_STRING);

    public static final Instant KEY_DELETES_AT_1 = KEY_EXPIRES_AT_1.plus(30, ChronoUnit.DAYS);

    public static final GeneratedKeyVo GENERATED_KEY_VO_1 = GeneratedKeyVo.builder()
            .keyHash(API_KEY_HASH_1)
            .keyId(KEY_ID_1)
            .keySuffix(API_KEY_SUFFIX_1)
            .formattedApiKey(API_KEY_1)
            .build();

    public static final KeyVo KEY_VO_1 = KeyVo.builder()
            .keyStatus(KeyStatus.ACTIVE)
            .ownerType(KeyOwnerType.USER)
            .ownerId(USER_KEY_OWNER_ID_1)
            .issuerServiceId(SERVICE_ID_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .keyId(KEY_ID_1)
            .keyHash(API_KEY_HASH_1)
            .createdAt(TEST_TIME)
            .expiresAt(KEY_EXPIRES_AT_1)
            .deletesAt(KEY_DELETES_AT_1)
            .apiKeySuffix(API_KEY_SUFFIX_1)
            .authorizations(KEY_AUTHZ_1)
            .description(KEY_DESCRIPTION_1)
            .build();

    public static final KeyByOwnerAndServiceVo KEY_BY_OWNER_AND_SERVICE_VO_1 =
            KeyByOwnerAndServiceVo.builder()
                    .keyStatus(KeyStatus.ACTIVE)
                    .ownerType(KeyOwnerType.USER)
                    .ownerId(USER_KEY_OWNER_ID_1)
                    .ownerStatus(KeyOwnerStatus.ACTIVE)
                    .ownerStatusUpdatedAt(TEST_TIME)
                    .issuerServiceId(SERVICE_ID_1)
                    .audienceServiceIds(Set.of(SERVICE_ID_1))
                    .keyId(KEY_ID_1)
                    .keyHash(API_KEY_HASH_1)
                    .createdAt(TEST_TIME)
                    .expiresAt(KEY_EXPIRES_AT_1)
                    .deletesAt(KEY_DELETES_AT_1)
                    .apiKeySuffix(API_KEY_SUFFIX_1)
                    .description(KEY_DESCRIPTION_1)
                    .build();

    public static String KEY_DETAILS_1_ENCRYPTED = "key-details-1-encrypted";

    public static final KeyByOwnerAndServiceModel KEY_BY_OWNER_AND_SERVICE_MODEL_1 =
            KeyByOwnerAndServiceModel.builder()
                    .ownerType(KeyOwnerType.USER)
                    .ownerId(USER_KEY_OWNER_ID_1)
                    .issuerServiceId(SERVICE_ID_1)
                    .keyId(KEY_ID_1)
                    .ownerStatus(KeyOwnerStatus.ACTIVE)
                    .ownerStatusUpdatedAt(TEST_TIME)
                    .expiresAt(KEY_EXPIRES_AT_1)
                    .deletesAt(KEY_DELETES_AT_1)
                    .keyStatus(KeyStatus.ACTIVE)
                    .keyDetails(KEY_DETAILS_1_ENCRYPTED)
                    .build();

    public static final KeyVo KEY_VO_1_AUTHZ_2 = KEY_VO_1.toBuilder()
            .authorizations(KEY_AUTHZ_2)
            .build();

    public static final CreateKeyRequestVo CREATE_KEY_REQUEST_VO =
            CreateKeyRequestVo.builder()
                    .generatedKeyVo(GENERATED_KEY_VO_1)
                    .key(KEY_VO_1)
                    .keyOwner(KEY_OWNER_VO_1)
                    .service(SERVICE_VO_1)
                    .build();

    public static final UpdateKeyRequestVo UPDATE_KEY_REQUEST_VO_1 =
            UpdateKeyRequestVo.builder()
                    .key(KEY_VO_1)
                    .keyOwner(KEY_OWNER_VO_1)
                    .service(SERVICE_VO_1)
                    .build();

    public static final ObjectNode AUTHORIZATION_JSON_NODES_1 =
            TEST_OM.createObjectNode()
                    .put("allow", true);

    public static final ObjectNode AUTHORIZATION_JSON_NODES_2 =
            TEST_OM.createObjectNode()
                    .put("allow", true)
                    .put("other_id", "other-id-value-1");

    public static final UpdateAuthorizationsRequest UPDATE_AUTHORIZATIONS_REQUEST_1 =
            UpdateAuthorizationsRequest.builder()
                    .authorizations(AUTHORIZATION_JSON_NODES_2)
                    .build();

    public static final UpdateKeyRequestVo UPDATE_KEY_REQUEST_VO_1_AUTHZ_2 =
            UPDATE_KEY_REQUEST_VO_1.toBuilder()
                    .key(KEY_VO_1_AUTHZ_2)
                    .authorizationsUpdated(true)
                    .build();

    public static final ListKeysRequestVo LIST_KEYS_REQUEST_VO_1 =
            ListKeysRequestVo.builder()
                    .keyOwner(KEY_OWNER_VO_1)
                    .service(SERVICE_VO_1)
                    .build();

    public static final DeleteKeyRequestVo DELETE_KEY_BY_ID_REQUEST_VO_1 =
            DeleteKeyRequestVo.builder()
                    .key(KEY_BY_OWNER_AND_SERVICE_VO_1)
                    .keyOwner(KEY_OWNER_VO_1)
                    .service(SERVICE_VO_1)
                    .build();

    public static final KeyDto KEY_DTO_1 = KeyDto.builder()
            .authorizations(AUTHORIZATION_JSON_NODES_1)
            .id(KEY_ID_1)
            .status(KeyStatus.ACTIVE)
            .ownerType(KeyOwnerType.USER)
            .ownerId(USER_KEY_OWNER_ID_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .issuerServiceId(SERVICE_ID_1)
            .description(KEY_DESCRIPTION_1)
            .createdAt(TEST_TIME)
            .expiresAt(KEY_EXPIRES_AT_1)
            .build();

    public static final KeyDto KEY_DTO_2 = KeyDto.builder()
            .authorizations(AUTHORIZATION_JSON_NODES_2)
            .id(KEY_ID_2)
            .status(KeyStatus.ACTIVE)
            .ownerType(KeyOwnerType.USER)
            .ownerId(USER_KEY_OWNER_ID_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .issuerServiceId(SERVICE_ID_1)
            .description(KEY_DESCRIPTION_2)
            .createdAt(TEST_TIME)
            .expiresAt(KEY_EXPIRES_AT_2)
            .build();

    public static final KeyDto KEY_DTO_1_SECRET = KEY_DTO_1.toBuilder()
            .value(API_KEY_1)
            .build();

    public static final KeyDto KEY_DTO_1_NO_SECRET = KEY_DTO_1.toBuilder()
            .value(API_KEY_SUFFIX_1)
            .build();

    public static final KeyDto KEY_DTO_1_NO_SECRET_NO_AUTHZ = KEY_DTO_1.toBuilder()
            .value(API_KEY_SUFFIX_1)
            .authorizations(null)
            .build();

    public static final KeyDto KEY_DTO_1_LOOKUP = KEY_DTO_1.toBuilder()
            .value(null)
            .authorizations(null)
            .build();

    public static final String KEY_REQUEST_1 = "{"
            + " \"expires_at\":\"" + KEY_EXPIRES_AT_1_STRING + "\","
            + " \"description\":\"" + KEY_DESCRIPTION_1 + "\","
            + " \"audience_service_ids\":[\"" + SERVICE_ID_1 + "\"],"
            + " \"authorizations\":" + KEY_AUTHZ_1
            + "}";

    public static final String KEY_REQUEST_2 = "{"
            + " \"expires_at\":\"" + KEY_EXPIRES_AT_2_STRING + "\","
            + " \"description\":\"" + KEY_DESCRIPTION_2 + "\","
            + " \"audience_service_ids\":[\"" + SERVICE_ID_1 + "\"],"
            + " \"authorizations\":" + KEY_AUTHZ_2
            + "}";

    public static final CreateKeyRequest CREATE_KEY_REQUEST_1 = CreateKeyRequest.builder()
            .expiresAt(KEY_EXPIRES_AT_1)
            .description(KEY_DESCRIPTION_1)
            .authorizations(AUTHORIZATION_JSON_NODES_1)
            .audienceServiceIds(Set.of(SERVICE_ID_1))
            .build();

    public static final ListKeysResponse LIST_KEYS_RESPONSE_1 = ListKeysResponse.builder()
            .keys(List.of(KEY_DTO_1_NO_SECRET_NO_AUTHZ))
            .build();

    public static final ListKeysResponse LIST_KEYS_RESPONSE_EMPTY = ListKeysResponse.builder()
            .keys(List.of())
            .build();

    // Records like this stay in the DB when all the keys removed from the user
    public static final KeyByOwnerAndServiceModel OWNER_INFO_ONLY_MODEL =
            KeyByOwnerAndServiceModel.builder()
                    .ownerId(USER_KEY_OWNER_ID_1)
                    .ownerStatus(KeyOwnerStatus.ACTIVE)
                    .ownerType(KeyOwnerType.USER)
                    .ownerStatusUpdatedAt(TEST_TIME)
                    .build();

    public static final IntrospectionRequest INTROSPECTION_REQUEST_1 = IntrospectionRequest
            .builder()
            .audienceServiceId(SERVICE_ID_1)
            .key(API_KEY_1)
            .build();

    public static final IntrospectionResponse INTROSPECTION_RESPONSE_1 =
            IntrospectionResponse.builder()
                    .authorizations(AUTHORIZATION_JSON_NODES_1)
                    .issuerServiceId(SERVICE_ID_1)
                    .ownerType(KeyOwnerType.USER)
                    .ownerId(USER_KEY_OWNER_ID_1)
                    .keyId(KEY_ID_1)
                    .build();

    public static final CachedIntrospectionResponse CACHED_INTROSPECTION_RESPONSE_1 =
            new CachedIntrospectionResponse(INTROSPECTION_RESPONSE_1, KEY_VO_1);

    public static final KeyLookupRequest KEY_LOOKUP_REQUEST_1 = KeyLookupRequest
            .builder()
            .key(API_KEY_1)
            .build();

    public static final UpdateKeyStatusRequest UPDATE_KEY_STATUS_REQUEST_1 =
            UpdateKeyStatusRequest.builder()
                    .status(KeyStatus.SUSPENDED)
                    .build();

    public static final SuspendKeysRequestVo SUSPEND_KEYS_REQUEST_VO_1 = SuspendKeysRequestVo
            .builder()
            .service(SERVICE_VO_1)
            .keyOwner(KEY_OWNER_VO_1)
            .build();

    public static final KeyOwnerDto KEY_OWNER_DTO_1 = KeyOwnerDto.builder()
            .ownerId(USER_KEY_OWNER_ID_1)
            .ownerType(KeyOwnerType.USER)
            .status(KeyOwnerStatus.ACTIVE)
            .statusUpdatedAt(TEST_TIME)
            .build();

    public static final UpdateKeyOwnerStatusRequest UPDATE_KEY_OWNER_STATUS_REQUEST_1 = new UpdateKeyOwnerStatusRequest(KeyOwnerStatus.ACTIVE);

}
