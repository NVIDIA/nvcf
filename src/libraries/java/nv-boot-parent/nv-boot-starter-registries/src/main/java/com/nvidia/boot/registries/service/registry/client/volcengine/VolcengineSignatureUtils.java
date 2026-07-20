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

package com.nvidia.boot.registries.service.registry.client.volcengine;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toCanonicalRequest;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toIsoBasicDateFormat;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toShortDateFormat;
import static com.nvidia.boot.registries.util.HashUtils.getSignatureKey;
import static com.nvidia.boot.registries.util.HashUtils.hmacSha256Hex;
import static com.nvidia.boot.registries.util.HashUtils.toSha256Hex;

import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineAuthHeaders;
import java.time.Instant;
import java.util.TreeMap;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class VolcengineSignatureUtils {

    public static final String ACTION_PARAM = "Action";
    public static final String VERSION_PARAM = "Version";
    public static final String API_VERSION = "2022-05-12";
    public static final String LIST_TAGS_ACTION = "ListTags";
    public static final String GET_AUTHORIZATION_TOKEN_ACTION = "GetAuthorizationToken";

    private static final String CREDENTIAL = "Credential";
    private static final String SIGNED_HEADERS = "SignedHeaders";
    private static final String SIGNATURE = "Signature";
    private static final String VOC_SIG_ALGORITHM = "HMAC-SHA256";
    private static final String HOST_HEADER = "host";
    private static final String X_DATE_HEADER = "x-date";
    private static final String X_CONTENT_SHA256_HEADER = "x-content-sha256";
    private static final String VOC_CR_SERVICE_NAME = "cr";
    private static final String VOC_REQUEST_TYPE = "request";
    private static final String POST_METHOD = "POST";
    private static final String ROOT_PATH = "/";

    public static VolcengineAuthHeaders signRequest(
            String hostname,
            String region,
            String accessKeyId,
            String secretAccessKey,
            String requestBody) {
        return signRequest(hostname, region, accessKeyId, secretAccessKey, requestBody, 
                          LIST_TAGS_ACTION);
    }

    public static VolcengineAuthHeaders signRequest(
            String hostname,
            String region,
            String accessKeyId,
            String secretAccessKey,
            String requestBody,
            String action) {
        var now = Instant.now();
        var volXDate = toIsoBasicDateFormat(now);
        var volShortXDate = toShortDateFormat(now);
        var contentSha256 = toSha256Hex(requestBody);

        var headers = new TreeMap<String, String>();
        headers.put(HOST_HEADER, hostname);
        headers.put(X_CONTENT_SHA256_HEADER, contentSha256);
        headers.put(X_DATE_HEADER, volXDate);

        var queryString = ACTION_PARAM + "=" + action
                + "&" + VERSION_PARAM + "=" + API_VERSION;
        var signedHeaders = String.join(";", headers.keySet());
        var canonicalRequest = toCanonicalRequest(POST_METHOD, ROOT_PATH, queryString,
                                                  headers, contentSha256);
        var credentialScope =
                volShortXDate + "/" + region + "/" + VOC_CR_SERVICE_NAME + "/" + VOC_REQUEST_TYPE;
        var stringToSign = VOC_SIG_ALGORITHM + "\n"
                + volXDate + "\n"
                + credentialScope + "\n"
                + toSha256Hex(canonicalRequest);
        var signingKey = getSignatureKey(secretAccessKey, volShortXDate, region,
                                         VOC_CR_SERVICE_NAME, VOC_REQUEST_TYPE);
        var signature = hmacSha256Hex(signingKey, stringToSign);
        var authorization = VOC_SIG_ALGORITHM + " " +
                CREDENTIAL + "=" + accessKeyId + "/" + credentialScope + ", " +
                SIGNED_HEADERS + "=" + signedHeaders + ", " +
                SIGNATURE + "=" + signature;
        return new VolcengineAuthHeaders(authorization, contentSha256, volXDate);
    }
}
