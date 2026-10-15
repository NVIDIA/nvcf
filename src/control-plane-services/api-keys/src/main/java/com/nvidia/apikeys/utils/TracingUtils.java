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

package com.nvidia.apikeys.utils;

import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.Span;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Collection;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.BooleanUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TracingUtils {

    public static final AttributeKey<String> KEY_OWNER_TYPE = AttributeKey.stringKey(
            "KEY_OWNER_TYPE");
    public static final AttributeKey<Boolean> KEY_OWNER_IS_NEW = AttributeKey.booleanKey(
            "KEY_OWNER_IS_NEW");
    public static final AttributeKey<String> KEY_OWNER_ID = AttributeKey.stringKey("KEY_OWNER_ID");
    public static final AttributeKey<String> KEY_OWNER_STATUS = AttributeKey.stringKey(
            "KEY_OWNER_STATUS");
    public static final AttributeKey<String> KEY_OWNER_STATUS_UPDATED_AT = AttributeKey.stringKey(
            "KEY_OWNER_STATUS_UPDATED_AT");
    public static final AttributeKey<String> INTROSPECTION_REQUEST_AUDIENCE = AttributeKey.stringKey(
            "INTROSPECTION_REQUEST_AUDIENCE");
    public static final AttributeKey<String> KEY_ID = AttributeKey.stringKey("KEY_ID");
    public static final AttributeKey<String> KEY_ISSUER = AttributeKey.stringKey("KEY_ISSUER");
    public static final AttributeKey<List<String>> KEY_AUDIENCE = AttributeKey.stringArrayKey(
            "KEY_AUDIENCE");
    public static final AttributeKey<String> KEY_SUFFIX = AttributeKey.stringKey("KEY_SUFFIX");
    public static final AttributeKey<Long> KEY_EXPIRES_IN_DAYS = AttributeKey.longKey(
            "KEY_EXPIRES_IN_DAYS");
    public static final AttributeKey<String> KEY_CREATED_AT = AttributeKey.stringKey(
            "KEY_CREATED_AT");

    private final Clock clock;


    public void addCustomTag(AttributeKey<Boolean> key, Boolean tagValue) {
        Span.current().setAttribute(key, BooleanUtils.isTrue(tagValue));
    }

    public void addCustomTag(AttributeKey<String> key, Object tagValue) {
        Span.current().setAttribute(key, getSafeStringValue(tagValue));
    }

    public void addCustomTag(AttributeKey<Long> key, long tagValue) {
        Span.current().setAttribute(key, tagValue);
    }

    public void addCustomTag(AttributeKey<List<String>> key, Collection<String> tagValue) {
        Span.current().setAttribute(key, tagValue == null ? List.of() : List.copyOf(tagValue));
    }

    public void addKeyOwnerTags(KeyOwnerVo keyOwnerVo, boolean isNewOwner) {
        try {
            addCustomTag(KEY_OWNER_ID, keyOwnerVo.getOwnerId());
            addCustomTag(KEY_OWNER_TYPE, keyOwnerVo.getOwnerType());
            addCustomTag(KEY_OWNER_STATUS, keyOwnerVo.getOwnerStatus());
            addCustomTag(KEY_OWNER_STATUS_UPDATED_AT, keyOwnerVo.getOwnerStatusUpdatedAt());
            addCustomTag(KEY_OWNER_IS_NEW, isNewOwner);
        } catch (Exception e) {
            log.error("Failed to add key owner tags", e);
        }
    }

    public void addIntrospectionRequestTags(IntrospectionRequest request) {
        try {
            addCustomTag(INTROSPECTION_REQUEST_AUDIENCE, request.getAudienceServiceId());
        } catch (Exception e) {
            log.error("Failed to add introspection request tags", e);
        }
    }

    public void addKeyTags(KeyVo keyVo) {
        try {
            addCustomTag(KEY_ID, keyVo.getKeyId());
            addCustomTag(KEY_ISSUER, keyVo.getIssuerServiceId());
            addCustomTag(KEY_AUDIENCE, keyVo.getAudienceServiceIds());
            addCustomTag(KEY_SUFFIX, keyVo.getApiKeySuffix());
            addCustomTag(KEY_OWNER_ID, keyVo.getOwnerId());
            addCustomTag(KEY_OWNER_TYPE, keyVo.getOwnerType());
            Instant now = clock.instant();
            if (keyVo.getExpiresAt() != null) {
                long expiresInDays = Duration.between(now, keyVo.getExpiresAt()).toDays();
                addCustomTag(KEY_EXPIRES_IN_DAYS, expiresInDays);
            }
            addCustomTag(KEY_CREATED_AT, keyVo.getCreatedAt());
        } catch (Exception e) {
            log.error("Failed to add introspection tags", e);
        }
    }

    public String getSafeStringValue(Object value) {
        return value == null ? "null" : value.toString();
    }
}
