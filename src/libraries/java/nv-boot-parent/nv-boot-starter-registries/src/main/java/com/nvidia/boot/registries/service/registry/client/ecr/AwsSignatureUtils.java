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

package com.nvidia.boot.registries.service.registry.client.ecr;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toCanonicalRequest;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toIsoBasicDateFormat;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toShortDateFormat;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.AMZ_CONTENT_TYPE;
import static com.nvidia.boot.registries.util.HashUtils.getSignatureKey;
import static com.nvidia.boot.registries.util.HashUtils.hmacSha256Hex;
import static com.nvidia.boot.registries.util.HashUtils.toSha256Hex;

import com.nvidia.boot.registries.service.registry.client.ecr.dto.AwsAuthHeaders;
import java.time.Instant;
import java.util.TreeMap;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class AwsSignatureUtils {

    private static final String CREDENTIAL = "Credential";
    private static final String SIGNED_HEADERS = "SignedHeaders";
    private static final String SIGNATURE = "Signature";
    private static final String AWS_SIG_ALGORITHM = "AWS4-HMAC-SHA256";
    private static final String AWS_REQUEST_TYPE = "aws4_request";
    private static final String HOST_HEADER = "host";
    private static final String X_AMZ_DATE_HEADER = "x-amz-date";
    private static final String X_AMZ_CONTENT_SHA256_HEADER = "x-amz-content-sha256";
    private static final String X_AMZ_TARGET_HEADER = "x-amz-target";
    private static final String CONTENT_TYPE_HEADER = "content-type";

    private static final String ECR_POST_METHOD = "POST";
    private static final String ECR_ROOT_PATH = "/";

    public static AwsAuthHeaders signRequest(
            String region,
            String service,
            String accessKeyId,
            String secretAccessKey,
            String requestBody,
            String target,
            String host) {
        var now = Instant.now();
        var amzDate = toIsoBasicDateFormat(now);
        var dateStamp = toShortDateFormat(now);
        var amzContentSha256 = toSha256Hex(requestBody);

        var headers = new TreeMap<String, String>();
        headers.put(CONTENT_TYPE_HEADER, AMZ_CONTENT_TYPE);
        headers.put(HOST_HEADER, host);
        headers.put(X_AMZ_CONTENT_SHA256_HEADER, amzContentSha256);
        headers.put(X_AMZ_DATE_HEADER, amzDate);
        headers.put(X_AMZ_TARGET_HEADER, target);

        var signedHeaders = String.join(";", headers.keySet());
        var canonicalRequest = toCanonicalRequest(ECR_POST_METHOD, ECR_ROOT_PATH, "",
                                                  headers, amzContentSha256);
        var credentialScope = dateStamp + "/" + region + "/" + service + "/" + AWS_REQUEST_TYPE;
        var stringToSign = AWS_SIG_ALGORITHM + "\n" +
                amzDate + "\n" +
                credentialScope + "\n" +
                toSha256Hex(canonicalRequest);
        var signingKey = getSignatureKey("AWS4" + secretAccessKey, dateStamp, region,
                                         service, AWS_REQUEST_TYPE);
        var signature = hmacSha256Hex(signingKey, stringToSign);
        var authorization = AWS_SIG_ALGORITHM + " " +
                CREDENTIAL + "=" + accessKeyId + "/" + credentialScope + ", " +
                SIGNED_HEADERS + "=" + signedHeaders + ", " +
                SIGNATURE + "=" + signature;
        return new AwsAuthHeaders(authorization, amzDate, amzContentSha256);
    }
}
