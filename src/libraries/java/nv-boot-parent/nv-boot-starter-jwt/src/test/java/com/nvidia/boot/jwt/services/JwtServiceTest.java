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

import static com.nvidia.boot.jwt.services.JwtService.ERROR_CANT_VALIDATE_EMPTY_TOKEN;
import static com.nvidia.boot.jwt.services.JwtService.ERROR_NO_JWK_SET_PROVIDED;
import static com.nvidia.boot.jwt.services.JwtService.ERROR_NO_KEY_REGISTERED_WITH_ID;
import static com.nvidia.boot.jwt.services.JwtService.ERROR_NO_SIGNER_DEFINED_FOR_KID;
import static com.nvidia.boot.jwt.services.JwtService.ERROR_TOKEN_DOESNT_HAVE_HEADER;
import static com.nvidia.boot.jwt.services.JwtService.ERROR_TOKEN_MISSING_KEY_ID_IN_HEADER;
import static com.nvidia.boot.jwt.services.JwtService.FAILED_TO_PARSE_TOKEN;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.when;

import com.google.common.collect.ImmutableMap;
import com.nimbusds.jose.EncryptionMethod;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.exceptions.EncryptionException;
import com.nvidia.boot.jwt.exceptions.JwtException;
import com.nvidia.boot.jwt.exceptions.ValidationException;
import java.io.IOException;
import java.security.InvalidAlgorithmParameterException;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.NoSuchAlgorithmException;
import java.security.interfaces.ECPrivateKey;
import java.security.interfaces.ECPublicKey;
import java.text.ParseException;
import java.util.UUID;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.core.io.ClassPathResource;

@ExtendWith(MockitoExtension.class)
class JwtServiceTest {

    private static final String TEST_A_256_GCM_KEY = "test-A256GCM-key";

    private JwtService jwtService;

    private JWKSet testKeySet;

    @Mock
    private SignedJWT signedJWTMock;

    private JweKeysMapping jweKeysMapping = JweKeysMapping
            .builder()
            .keysMapping(ImmutableMap.of(
                    "test-A256GCM-key", "test-A256GCM-key"
            ))
            .build();

    @BeforeEach
    void init()
            throws IOException, ParseException, JOSEException {
        ClassPathResource keyResource = new ClassPathResource("keys/dev_jwks_private.json");
        testKeySet = JWKSet.load(keyResource.getInputStream());
        jwtService = new JwtService(testKeySet, jweKeysMapping);
    }

    @Test
    void shouldThrowIfKeySetEmpty() {
        IllegalStateException exception = assertThrows(
                IllegalStateException.class,
                () -> new JwtService(new JWKSet(), jweKeysMapping));

        assertThat(exception.getMessage()).isEqualTo(ERROR_NO_JWK_SET_PROVIDED);
    }

    @Test
    void shouldEncryptAndDecrypt() {
        String payload = "test message";
        String serializedJwe = jwtService
                .encryptWithKeysetName(TEST_A_256_GCM_KEY, payload);

        // verify we can decrypt it
        String decryptedPayload = jwtService.decrypt(serializedJwe);
        assertEquals(payload, decryptedPayload);
    }

    @Test
    void shouldThrowIfWrongKey() {
        String kid = "wrong-key";

        EncryptionException exception = assertThrows(
                EncryptionException.class,
                () -> jwtService.encryptWithKeyId(kid, "payload"));
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "No encrypter defined for kid:" + kid);
    }

    @Test
    void shouldThrowIfCantParse() {
        EncryptionException exception = assertThrows(
                EncryptionException.class,
                () -> jwtService.decrypt("not-jwe-at-all"));
        assertThat(exception.getBody().getDetail()).startsWith("Can't parse encrypted jwe string:");
    }

    @Test
    void shouldThrowIfCantFindKey() {
        // test message that was encrypted with the key
        //    {
        //      "kty": "oct",
        //      "kid": "im-key-that-has-been-deleted",
        //      "k": "2AIyuwTgBgF_yUuopz2Y8KP7hrM5wf8veHGCqR8iI3U",
        //      "alg": "A256GCM"
        //    }
        String payload = "eyJraWQiOiJpbS1rZXktdGhhdC1oYXMtYmVlbi1kZWxldGVkIiwiZW5jIjoiQTI1NkdDT"
                + "SIsInRhZyI6InJaSzJrLVR3TXBla2tnLXE3eUxsSXciLCJhbGciOiJBMjU2R0NNS1ciLCJpdiI6I"
                + "ncwWXd0Wi01N2d2eFZkWUEifQ.dXolUep0AjT91J2HnRkAUaWZHpwdpUCPMXwYvvvqP6E.PLFFG_"
                + "q8ygCcMk2A.nzI_SEHmayPI7Yvq.cJ6TgGNCmvc-tmH6fzCqMA";
        EncryptionException exception = assertThrows(
                EncryptionException.class,
                () -> jwtService.decrypt(payload));
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Can't decrypt JWT, no decrypter defined for key: im-key-that-has-been-deleted");
    }

    @Test
    void shouldThrowIfNoHeader() {
        // specially fabricated payload without kid
        String payloadNoKid = "eyJlbmMiOiJBMjU2R0NNIiwidGFnIjoib3BfbHk4eUtWUHRBTzBjOXhtVi1ndyIs"
                + "ImFsZyI6IkEyNTZHQ01LVyIsIml2IjoiV2dyRV9EVi02WmtFQno3ViJ9.M9KA2k7epZwUTlwJeO5pfB"
                + "F3tN4ei9slulWJ14NH0Uw.OUFn-3f0wcdfj3Nl.SuWueOtxRoHe0K50.E08Jm9r7sEMDe0cpqwb-fA";
        EncryptionException exception = assertThrows(
                EncryptionException.class,
                () -> jwtService.decrypt(payloadNoKid));
        assertThat(exception.getBody().getDetail()).isEqualTo("JWT is missing kid in header");
    }

    @Test
    void shouldThrowIfEncryptedMessageWasTemperedWith() {
        String payload = "eyJraWQiOiJ0ZXN0LUEyNTZHQ00ta2V5IiwiZW5jIjoiQTI1NkdDTSIsInRhZyI6IlhlbnA4"
                + "dGtZVzBORW1ZemRlY3JSSVEiLCJhbGciOiJBMjU2R0NNS1ciLCJpdiI6IlV3blhOdkNyQ1BXdlhRUlA"
                + "ifQ.9A2u4RYiH6spVwsG3hUA5LgotchamofohqFSspwXbgbNK9Qfly8U8.TyDIcwS-Ri1-db8c.uTqa"
                + "nt7gTetyCt5R.vjLzlRpP_zu4PTFx1T4HDg";
        EncryptionException exception = assertThrows(
                EncryptionException.class,
                () -> jwtService.decrypt(payload));
        assertThat(exception.getBody().getDetail()).startsWith(
                "Failed to decrypt JWT, error was: ");
    }

    @Test
    void shouldThrowWhenValidatingNull() {
        ValidationException exception = assertThrows(
                ValidationException.class,
                () -> jwtService.validateSignature(null));
        assertThat(exception.getBody().getDetail()).isEqualTo(ERROR_CANT_VALIDATE_EMPTY_TOKEN);
    }

    @Test
    void shouldThrowWhenHeaderNull() {
        when(signedJWTMock.getHeader()).thenReturn(null);
        ValidationException exception = assertThrows(
                ValidationException.class,
                () -> jwtService.validateSignature(signedJWTMock));
        assertThat(exception.getBody().getDetail()).isEqualTo(ERROR_TOKEN_DOESNT_HAVE_HEADER);
    }

    @Test
    void shouldThrowIfKidUnset() {
        JWSHeader jwsHeader = new JWSHeader.Builder(JWSAlgorithm.ES512).build();
        when(signedJWTMock.getHeader()).thenReturn(jwsHeader);
        ValidationException exception = assertThrows(
                ValidationException.class,
                () -> jwtService.validateSignature(signedJWTMock));
        assertThat(exception.getBody().getDetail()).isEqualTo(ERROR_TOKEN_MISSING_KEY_ID_IN_HEADER);
    }

    @Test
    void shouldThrowIfNoJwsVerifierRegistered() {
        JWSHeader jwsHeader = new JWSHeader.Builder(JWSAlgorithm.ES512)
                .keyID("non-existing-key").build();

        when(signedJWTMock.getHeader()).thenReturn(jwsHeader);

        ValidationException exception = assertThrows(
                ValidationException.class,
                () -> jwtService.validateSignature(signedJWTMock));
        assertThat(exception.getBody().getDetail()).startsWith(ERROR_NO_KEY_REGISTERED_WITH_ID);
    }

    // Use this piece to generate new keys
    @Test
    void aesKeygen()
            throws NoSuchAlgorithmException {
        // Generate a secret AES key with 256 bits
        KeyGenerator gen = KeyGenerator.getInstance("AES");
        gen.init(256);
        SecretKey aesKey = gen.generateKey();

        // Convert to JWK format
        JWK jwk = new OctetSequenceKey.Builder(aesKey)
                .keyID(UUID.randomUUID().toString())
                .algorithm(EncryptionMethod.A256GCM)
                .build();

        assertNotNull(jwk);
        System.out.println(jwk.toJSONString());
    }

    @Test
    void ecKeygen()
            throws NoSuchAlgorithmException, InvalidAlgorithmParameterException {
        // Generate EC key pair with P-256 curve
        KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
        gen.initialize(Curve.P_256.toECParameterSpec());
        KeyPair keyPair = gen.generateKeyPair();

        // Convert to JWK format
        JWK jwk = new ECKey.Builder(Curve.P_256, (ECPublicKey) keyPair.getPublic())
                .privateKey((ECPrivateKey) keyPair.getPrivate())
                .build();

        assertNotNull(jwk);
        System.out.println(jwk.toJSONString());
    }

    @Test
    void signJwt()
            throws ParseException {
        JWTClaimsSet.Builder claimsSetBuilder = new JWTClaimsSet.Builder();
        String jti = UUID.randomUUID().toString();
        claimsSetBuilder.jwtID(jti);

        JWSAlgorithm jwsAlgorithm = JWSAlgorithm.parse("ES256");
        String tokenKid = "dev-key-id";

        // prepare token for signing
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder(jwsAlgorithm)
                        .keyID(tokenKid)
                        .build(),
                claimsSetBuilder.build());

        jwtService.signJwt(signedJWT, tokenKid);

        // Serialize the JWS to compact form
        String serializedJwt = signedJWT.serialize();

        SignedJWT parsedJwt = jwtService.getSignedJWT(serializedJwt);
        assertThat(jwtService.validateSignature(parsedJwt)).isTrue();

        JWTClaimsSet jwtClaimsSet = jwtService.getJwtClaimsSet(serializedJwt);
        assertThat(jwtClaimsSet.getClaim("jti")).isEqualTo(jti);
    }

    @Test
    void shouldThrowIfSigningKeyDoesntExist() {
        String keyId = "nope";

        IllegalArgumentException exception = assertThrows(
                IllegalArgumentException.class,
                () -> jwtService.signJwt(null, keyId));

        assertThat(exception.getMessage()).isEqualTo(ERROR_NO_SIGNER_DEFINED_FOR_KID + keyId);
    }

    @Test
    void shouldThrowOnBadTokens() {
        JwtException exception = assertThrows(
                JwtException.class,
                () -> jwtService.getSignedJWT("sick"));
        assertThat(exception.getBody().getDetail()).isEqualTo(FAILED_TO_PARSE_TOKEN);
    }
}
