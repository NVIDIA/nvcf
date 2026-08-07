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
package com.nvidia.ess.encryption.crypto;

import com.google.common.annotations.VisibleForTesting;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.KeyLengthException;
import com.nimbusds.jose.Payload;
import com.nimbusds.jose.crypto.DirectEncrypter;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import com.nvidia.ess.encryption.integrity.IntegrityChecks;
import com.nvidia.ess.encryption.integrity.IntegrityChecksPopulationFields;
import com.nvidia.ess.encryption.integrity.IntegrityChecksValidationFields;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.util.EncryptionKeyGenerator;
import java.text.ParseException;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class MekService {
    @Setter(onMethod_ = {@Autowired})
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @Setter(onMethod_ = {@Autowired})
    private IntegrityChecks integrityChecks;

    @VisibleForTesting
    Payload getPayload(String data) {
        return new Payload(data);
    }

    @VisibleForTesting
    JWEObject getJWEObject(JWEHeader header, Payload payload) {
        return new JWEObject(header, payload);
    }

    @VisibleForTesting
    DirectEncrypter getDirectEncrypter(OctetSequenceKey key)
            throws KeyLengthException {
        return new DirectEncrypter(key);
    }
    // Encrypt a string data and IntegrityCheckFields and return and stringified JWEObject.
    public String encryptWithIntegrityCheck(OctetSequenceKey key, String data, IntegrityChecksPopulationFields icFields) throws
            JOSEException {
        JWEHeader.Builder builder = CryptoService.getJWEBuilder(key, data);
        // populate header with IC fields
        JWEHeader header =  integrityChecks.populateIfEnabled(builder, icFields).build();
        // Set the payload.
        Payload payload = getPayload(data);

        // Create the JWE object and encrypt it
        JWEObject jweObject = getJWEObject(header, payload);
        jweObject.encrypt(getDirectEncrypter(key));

        // Serialise to compact JOSE form.
        return jweObject.serialize();
    }


    public OctetSequenceKey extractKey(EncryptionKeyModel encryptionKeyModel)
            throws ParseException, JOSEException {

        String timestampedKey =
                decryptWithMasterKey(encryptionKeyModel.getEncryptedKey());

        OctetSequenceKey extractedKey = EncryptionKeyGenerator.generateEncryptionKey(timestampedKey);

        IntegrityChecksValidationFields icvFields = new IntegrityChecksValidationFields(encryptionKeyModel.getNamespace(),
                encryptionKeyModel.getCreatedAt(),
                encryptionKeyModel.getEncryptedAt(),
                encryptionKeyModel.getKid(),
                encryptionKeyModel.getEncryptedByKid());

        integrityChecks.validateIfEnabled(encryptionKeyModel.getEncryptedKey(), extractedKey, icvFields);


        return extractedKey;
    }

    private String decryptWithMasterKey(String encryptedString)
            throws ParseException, JOSEException {
        JWEObject jweObject = JWEObject.parse(encryptedString);
        OctetSequenceKey mek = getMasterDecryptionKey(jweObject.getHeader().getKeyID());

        // Get the decrypted payload as string.
        return CryptoService.decrypt(mek, encryptedString);
    }

    private OctetSequenceKey getMasterDecryptionKey(String kid) {
        if (!cryptoPropertiesHolder.get().getParsedAllMasterKeys().containsKey(kid)) {
            // 500 as internal MEK is missing
            log.error("Master key does not exist corresponding to KeyId: " + kid);
            throw new MissingMasterKeyException("Failed to get master encryption key");
        }
        return cryptoPropertiesHolder.get().getParsedAllMasterKeys().get(kid);
    }
}
