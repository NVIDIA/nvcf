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

package com.nvidia.boot.jwt.services;

import static org.springframework.util.CollectionUtils.isEmpty;

import com.google.common.annotations.VisibleForTesting;
import com.google.common.base.Strings;
import com.nimbusds.jose.EncryptionMethod;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWEAlgorithm;
import com.nimbusds.jose.JWEDecrypter;
import com.nimbusds.jose.JWEEncrypter;
import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.JWSVerifier;
import com.nimbusds.jose.Payload;
import com.nimbusds.jose.crypto.AESDecrypter;
import com.nimbusds.jose.crypto.AESEncrypter;
import com.nimbusds.jose.crypto.ECDHDecrypter;
import com.nimbusds.jose.crypto.ECDHEncrypter;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.crypto.ECDSAVerifier;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jose.crypto.MACVerifier;
import com.nimbusds.jose.crypto.RSADecrypter;
import com.nimbusds.jose.crypto.RSAEncrypter;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.crypto.RSASSAVerifier;
import com.nimbusds.jose.crypto.bc.BouncyCastleProviderSingleton;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jose.util.Base64URL;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.exceptions.EncryptionException;
import com.nvidia.boot.jwt.exceptions.JwtException;
import com.nvidia.boot.jwt.exceptions.SigningException;
import com.nvidia.boot.jwt.exceptions.ValidationException;
import io.opentelemetry.api.trace.Span;
import java.text.ParseException;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;

@Slf4j
public class JwtService {

    public static final String FAILED_TO_PARSE_TOKEN = "Failed to parse token";
    public static final String ERROR_CANT_VALIDATE_EMPTY_TOKEN = "Can't validate empty token";
    public static final String ERROR_TOKEN_DOESNT_HAVE_HEADER = "Token doesn't have header";
    public static final String ERROR_TOKEN_MISSING_KEY_ID_IN_HEADER = "Token missing key id in header";
    public static final String ERROR_NO_KEY_REGISTERED_WITH_ID = "No key registered with id:";
    public static final String ENCRYPTION_KID_TAG = "JWE_KEY_ID";
    public static final String ERROR_NO_JWK_SET_PROVIDED = "No JWKSet provided";
    public static final String ERROR_ENCRYPTION_KEY_MAPPING_NOT_SET = "Encryption key mapping is not set in config file.";
    public static final String ERROR_NO_SIGNER_DEFINED_FOR_KID = "No signer defined for kid:";
    /**
     * Both signers and verifiers in the library are thread safe and use factories under the hood.
     * Pooling or caching unlikely to improve performance.
     */
    // Signers accessible by key id
    private final Map<String, JWSSigner> signersByKidMap = new HashMap<>();

    // Verifiers accessible by key id
    private final Map<String, JWSVerifier> verifiersByKidMap = new HashMap<>();

    // Encrypters accessible by key id
    private final Map<String, JWEEncrypter> encryptersByKidMap = new HashMap<>();

    // Decrypters accessible by key id
    private final Map<String, JWEDecrypter> decryptersByKidMap = new HashMap<>();

    private static final EncryptionMethod encryptionMethod = EncryptionMethod.A256GCM;
    private static final JWEAlgorithm jweAlgorithm = JWEAlgorithm.A256GCMKW;

    private final Map<String, String> keyNameToIdMap;

    /**
     * Build this service based on the given keystore. All keys must have a key id ({@code kid})
     * field in order to be used.
     *
     * @param jwkSet the key set to load all keys from
     */
    @Autowired
    public JwtService(
            @NonNull JWKSet jwkSet,
            JweKeysMapping jweKeysMapping)
            throws JOSEException {
        List<JWK> jwkSetKeys = jwkSet.getKeys();
        if (isEmpty(jwkSetKeys)) {
            throw new IllegalStateException(ERROR_NO_JWK_SET_PROVIDED);
        }
        for (JWK key : jwkSetKeys) {
            if (Strings.isNullOrEmpty(key.getKeyID())) {
                // halt service
                throw new IllegalStateException("One of the keys in key set has no id.");
            }

            if (key instanceof RSAKey rsaKey) {
                registerKey(rsaKey);
            } else if (key instanceof ECKey ecKey) {
                registerKey(ecKey);
            } else if (key instanceof OctetSequenceKey octetSequenceKey) {
                registerKey(octetSequenceKey);
            } else {
                throw new IllegalArgumentException("Only RSA, EC and MAC keys supported so far");
            }
        }

        if (isEmpty(jweKeysMapping.getKeysMapping())) {
            throw new IllegalStateException(ERROR_ENCRYPTION_KEY_MAPPING_NOT_SET);
        }
        this.keyNameToIdMap = jweKeysMapping.getKeysMapping();
    }

    private void registerKey(OctetSequenceKey key)
            throws JOSEException {
        addSignerVerifier(key);
        addEncrypterDecrypter(key);
    }

    private void registerKey(ECKey key)
            throws JOSEException {
        addSignerVerifier(key);
        addEncrypterDecrypter(key);
    }

    private void registerKey(RSAKey key)
            throws JOSEException {
        addSignerVerifier(key);
        addEncrypterDecrypter(key);
    }

    private void addSignerVerifier(RSAKey key)
            throws JOSEException {
        if (key.isPrivate()) {
            RSASSASigner signer = new RSASSASigner(key);
            signersByKidMap.put(key.getKeyID(), signer);
        }

        RSASSAVerifier verifier = new RSASSAVerifier(key);
        verifiersByKidMap.put(key.getKeyID(), verifier);
    }

    private void addSignerVerifier(ECKey key)
            throws JOSEException {
        if (key.isPrivate()) {
            ECDSASigner signer = new ECDSASigner(key);
            signersByKidMap.put(key.getKeyID(), signer);
        }

        ECDSAVerifier verifier = new ECDSAVerifier(key);
        verifiersByKidMap.put(key.getKeyID(), verifier);
    }

    private void addSignerVerifier(OctetSequenceKey key)
            throws JOSEException {
        // its symmetric thus both public and private
        MACSigner signer = new MACSigner(key);
        signersByKidMap.put(key.getKeyID(), signer);

        MACVerifier verifier = new MACVerifier(key);
        verifiersByKidMap.put(key.getKeyID(), verifier);
    }

    /**
     * Signs the JWT with selected key and algorithm
     *
     * @param jwt token to sign
     * @param keyId id of private key to use for signing
     */
    public void signJwt(SignedJWT jwt, String keyId) {
        addCustomTag(ENCRYPTION_KID_TAG, keyId);
        if (!signersByKidMap.containsKey(keyId)) {
            throw new IllegalArgumentException(ERROR_NO_SIGNER_DEFINED_FOR_KID + keyId);
        }

        JWSSigner signer = signersByKidMap.get(keyId);

        try {
            jwt.sign(signer);
        } catch (JOSEException e) {
            throw new SigningException("Error while signing token", e);
        }
        log.debug("Signed token using kid: {}", keyId);
    }

    /**
     * Validates token signature using registered keys
     *
     * @param jwt token to verify
     * @return whether token is valid
     */
    public boolean validateSignature(SignedJWT jwt) {

        boolean result = false;

        if (jwt == null) {
            throw new ValidationException(ERROR_CANT_VALIDATE_EMPTY_TOKEN);
        }

        if (jwt.getHeader() == null) {
            throw new ValidationException(ERROR_TOKEN_DOESNT_HAVE_HEADER);
        }

        String kid = jwt.getHeader().getKeyID();
        if (Strings.isNullOrEmpty(kid)) {
            throw new ValidationException(ERROR_TOKEN_MISSING_KEY_ID_IN_HEADER);
        }

        addCustomTag(ENCRYPTION_KID_TAG, kid);
        log.debug("verifying token using kid: {}", kid);
        JWSVerifier jwsVerifier = verifiersByKidMap.get(kid);
        if (jwsVerifier == null) {
            throw new ValidationException(ERROR_NO_KEY_REGISTERED_WITH_ID + kid);
        }

        try {
            if (jwt.verify(jwsVerifier)) {
                result = true;
            }
        } catch (JOSEException e) {
            log.error("Failed to validate signature with {} error message: {}", jwsVerifier, e
                    .getMessage());
        }
        return result;
    }

    private void addEncrypterDecrypter(RSAKey key)
            throws JOSEException {
        RSAEncrypter encrypter = new RSAEncrypter(key);
        String kid = key.getKeyID();

        if (key.isPrivate()) {
            RSADecrypter decrypter = new RSADecrypter(key);
            addEncrypterDecrypter(kid, encrypter, decrypter);
        } else {
            addEncrypter(kid, encrypter);
            log.warn("No private key for kid: {}", kid);
        }
    }

    private void addEncrypterDecrypter(ECKey key)
            throws JOSEException {
        ECDHEncrypter encrypter = new ECDHEncrypter(key);
        String kid = key.getKeyID();

        if (key.isPrivate()) {
            ECDHDecrypter decrypter = new ECDHDecrypter(key);
            addEncrypterDecrypter(kid, encrypter, decrypter);
        } else {
            addEncrypter(kid, encrypter);
            log.warn("No private key for kid: {}", kid);
        }
    }

    private void addEncrypterDecrypter(OctetSequenceKey key)
            throws JOSEException {
        addEncrypterDecrypter(key.getKeyID(), new AESEncrypter(key), new AESDecrypter(key));
    }

    private void addEncrypterDecrypter(String kid, JWEEncrypter encrypter, JWEDecrypter decrypter) {
        addEncrypter(kid, encrypter);
        decrypter.getJCAContext().setProvider(BouncyCastleProviderSingleton.getInstance());
        decryptersByKidMap.put(kid, decrypter);
    }

    private void addEncrypter(String kid, JWEEncrypter encrypter) {
        encrypter.getJCAContext().setProvider(BouncyCastleProviderSingleton.getInstance());
        encryptersByKidMap.put(kid, encrypter);
    }

    // Visible to be used in tests to validate values in token.
    public JWTClaimsSet getJwtClaimsSet(String token)
            throws ParseException {
        SignedJWT signedJWT = getSignedJWT(token);
        return signedJWT.getJWTClaimsSet();
    }

    @VisibleForTesting
    SignedJWT getSignedJWT(String token) {
        SignedJWT signedJWT = null;
        try {
            signedJWT = SignedJWT.parse(token);
        } catch (ParseException e) {
            throw new JwtException(FAILED_TO_PARSE_TOKEN, e);
        }

        if (!validateSignature(signedJWT)) {
            throw new JwtException("Cant de-tokenize due to invalid signature");
        }
        return signedJWT;
    }

    /**
     * Encrypts payload with key from named key set
     *
     * @param keySetName name of the key set
     * @param payload payload to encryptWithKeysetName
     * @return encrypted payload
     */
    public String encryptWithKeysetName(String keySetName, String payload) {
        return encryptWithKeyId(getKid(keySetName), payload);
    }

    /**
     * Encrypts string payload with selected key
     */
    public String encryptWithKeyId(String kid, String stringPayload) {
        addCustomTag(ENCRYPTION_KID_TAG, kid);

        if (!encryptersByKidMap.containsKey(kid)) {
            throw new EncryptionException("No encrypter defined for kid:" + kid);
        }

        JWEHeader jweHeader = new JWEHeader.Builder(jweAlgorithm, encryptionMethod)
                .keyID(kid)
                .build();

        Payload payload = new Payload(Base64URL.encode(stringPayload));
        JWEObject jweObject = new JWEObject(jweHeader, payload);

        JWEEncrypter encrypter = encryptersByKidMap.get(kid);
        try {
            jweObject.encrypt(encrypter);
        } catch (JOSEException e) {
            throw new EncryptionException(
                    "Failed to encryptWithKeysetName JWT, error is: " + e.getMessage(), e);
        }
        return jweObject.serialize();
    }

    /**
     * Decrypts JWT and returns payload
     *
     * @param encryptedJweString string token to decrypt
     */
    public String decrypt(String encryptedJweString) {
        JWEObject jweObject;
        try {
            jweObject = JWEObject.parse(encryptedJweString);
        } catch (ParseException e) {
            throw new EncryptionException("Can't parse encrypted jwe string:" + e.getMessage(), e);
        }

        JWEHeader header = jweObject.getHeader();

        if (header == null || Strings.isNullOrEmpty(header.getKeyID())) {
            throw new EncryptionException("JWT is missing kid in header");
        }
        String kid = header.getKeyID();
        addCustomTag(ENCRYPTION_KID_TAG, kid);
        JWEDecrypter decrypter = decryptersByKidMap.get(kid);

        if (decrypter == null) {
            throw new EncryptionException("Can't decrypt JWT, no decrypter defined for key: " +
                                                  kid);
        }

        try {
            jweObject.decrypt(decrypter);
        } catch (JOSEException e) {
            throw new EncryptionException("Failed to decrypt JWT, error was: ", e);
        }

        // Payload is never null, it's set in jwt.decrypt()
        return jweObject.getPayload().toString();
    }

    private String getKid(String keySetName) {
        String kid = keyNameToIdMap.get(keySetName);
        if (kid == null) {
            throw new EncryptionException("Keyset " + keySetName + " is not configured");
        }
        return kid;
    }

    private static void addCustomTag(String tagName, String tagValue) {
        Span.current().setAttribute(tagName, tagValue != null ? tagValue : "UNKNOWN");
    }
}
