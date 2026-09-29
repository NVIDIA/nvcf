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
package com.nvidia.ess.encryption.integrity;

import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.Constants;
import com.nvidia.ess.encryption.constants.IntegrityChecksKeys;
import com.nvidia.ess.encryption.exceptions.IntegrityChecksValidationException;
import java.text.ParseException;
import java.util.Objects;
import lombok.NonNull;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class IntegrityChecks {

    /**
     * Autowiring of encryption properties so that feature flags can be checked
     */
    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    /**
     * If integrity checks population is enabled then this method adds custom headers in the JWE header part of  WE Object
     * VERSION = 1.0 is added to mark 1.0 schema of headers
     * Additional headers added are : NAMESPACE, CREATED_AT and ENCRYPTED_AT
     * @param builder - Handle to JWEHeader builder object. Using this custom headers will be added.
     * @param icFields - Integrity check fields using which custom headers will be added in the header
     * @return - builder
     */

    public JWEHeader.Builder populateIfEnabled(@NonNull JWEHeader.Builder builder, @NonNull
    IntegrityChecksPopulationFields icFields) {

        // gate that checks feature flags
        if (!encryptionProperties.getIntegrityChecks().isPopulationEnabled()) {
            log.debug("skipping the integrity checks population");
            return builder;
        }
        log.debug("populating integrity checks for (namespace: {}, encryptedAt: {})",
                icFields.namespace(), icFields.encryptedAt());

        return builder
                    .customParam(IntegrityChecksKeys.VERSION, IntegrityChecksKeys.VERSION_1)
                    .customParam(IntegrityChecksKeys.NAMESPACE, icFields.namespace())
                    .customParam(IntegrityChecksKeys.CREATED_AT, icFields.createdAt().toString())
                    .customParam(IntegrityChecksKeys.ENCRYPTED_AT,
                            icFields.encryptedAt().toEpochMilli());
    }


    private static void checkNull(Object obj, String key) {
        if (Objects.isNull(obj)) {
            throw new IntegrityChecksValidationException(String.format(Constants.MSG_OBJ_NULL, key));
        }
    }

    public static <T> void isInstanceOf(Object obj, Class<T> classType, String key) {
        if (!classType.isInstance(obj)) {
            throw new IntegrityChecksValidationException(String.format(Constants.MSG_TYPE_MISMATCH, key, obj.getClass().getName(), classType.getName()));
        }
    }

    private static <T> void matchFields(Class<T> classType, Object objA, Object objB, String messageTemplate) {

        if (classType == String.class) {
            String objAs = (String)objA;
            String objBs = (String)objB;
            if (!(objAs.equals(objBs))) {
                throw new IntegrityChecksValidationException(String.format(messageTemplate, objA, objB));
            }
        } else if (classType == Long.class && ((long)objA != (long)objB)) {
            throw new IntegrityChecksValidationException(String.format(messageTemplate, objA, objB));
        }
    }

    private static <T> void checkAndMatch(Object objA, String key, Class<T> classType, Object objB, String messageTemplate) {

        // check
        checkNull(objA, key);
        checkNull(objB, key);
        isInstanceOf(objA, classType, key);
        isInstanceOf(objB, classType, key);

        // match
        matchFields(classType, objA, objB, messageTemplate);
    }

    private static <T> void extractCheckAndMatch(JWEHeader header, String key, Class<T> classType, Object objB, String messageTemplate) {
        // extract
        Object objA = header.getCustomParam(key);
        checkAndMatch(objA, key, classType, objB, messageTemplate);
    }


        /**
         * If Validation is enabled, performs Integrity Checks Validations.
         * If Validation succeeds then return void else it throws exceptions
         * Note: if icFields objects does not contain VERSION key it skips the validation to support older keys
         *
         * @param encryptedNEK: String object that contains encrypted NEK string
         * @param decryptedNEK: OctetSequenceKey object that contains decrypted NEK
         * @param icFields: Integrity Checks fields that will be used for validation
         * @throws IntegrityChecksValidationException - if validation for integrity checks fail due to mismatch or missing headers or parsing errors
         */

    public void validateIfEnabled(@NonNull String encryptedNEK, @NonNull OctetSequenceKey decryptedNEK, @NonNull IntegrityChecksValidationFields icFields) {
        // gate that checks feature flags
        if (!encryptionProperties.getIntegrityChecks().isValidationEnabled()) {
            log.debug("skipping the integrity checks validation");
            return;
        }
        log.debug("validating integrity checks for (namespace {}, kid: {}, encryptedAt: {})",
                icFields.namespace(), maskKid(icFields.kid()), icFields.encryptedAt());

        JWEObject jweObject;
        try {
            jweObject = JWEObject.parse(encryptedNEK);
        } catch (ParseException e) {
            log.error("failed to parse encrypted NEK", e);
            throw new IntegrityChecksValidationException(e.getMessage());
        }
        JWEHeader header = jweObject.getHeader();

        var version = header.getCustomParam(IntegrityChecksKeys.VERSION);
        // for backward compatibility, validation always succeeds for older keys
        if (Objects.isNull(version)) {
            log.debug("version key not found in JWE header. skipping the integrity checks validation for (namespace {}, kid: {}, encryptedAt: {})",
                    icFields.namespace(), maskKid(icFields.kid()), icFields.encryptedAt());
            return;
        }

        // extract ns
        extractCheckAndMatch(header, IntegrityChecksKeys.NAMESPACE, String.class, icFields.namespace(), Constants.MSG_NAMESPACE_MISMATCHED);
        // extract createdAt
        extractCheckAndMatch(header, IntegrityChecksKeys.CREATED_AT, String.class, icFields.createdAt().toString(), Constants.MSG_CREATED_AT_MISMATCHED);
        // extract encryptedAt
        extractCheckAndMatch(header, IntegrityChecksKeys.ENCRYPTED_AT, Long.class, icFields.encryptedAt().toEpochMilli(), Constants.MSG_ENCRYPTED_AT_MISMATCHED);
        // extract encryptedByKid
        checkAndMatch(header.getKeyID(), IntegrityChecksKeys.ENCRYPTED_BY_KID, String.class, icFields.encryptedByKid(), Constants.MSG_ENCRYPTED_BY_KID_MISMATCHED);
        // kid is extracted from decrypted key thumbprint
        checkAndMatch(decryptedNEK.getKeyID(), IntegrityChecksKeys.KID, String.class, icFields.kid(), Constants.MSG_KID_MISMATCHED);

    }
}
