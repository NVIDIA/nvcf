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
package com.nvidia.ess.auth;

import static org.junit.jupiter.api.Assertions.assertSame;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.ess.auth.jwt.JwtClaimValidator;
import com.nvidia.ess.auth.jwt.JwtSignatureValidationService;
import com.nvidia.ess.constants.AuthRoles;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.facade.AuthorizationsFacade;
import java.text.ParseException;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockedStatic;
import org.mockito.Mockito;
import org.mockito.MockitoAnnotations;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.aot.DisabledInAotMode;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.Context;

@ContextConfiguration(classes = {AuthChecker.class, AuthProperties.class,
        OperatorProperties.class,
        NotaryProperties.class})
@ExtendWith(SpringExtension.class)
@DisabledInAotMode
// TODO: Add more UTs
class AuthCheckerTest {

    @MockitoBean
    private JwtSignatureValidationService jwtSignatureValidationService;

    @MockitoBean
    private AuthorizationsFacade authorizationsFacade;

    @Mock
    private AuthProperties authProperties;

    @Mock
    private OperatorProperties essOperatorProperties;

    @Mock
    private NotaryProperties notaryProperties;

    @InjectMocks
    private AuthChecker authChecker;

    @BeforeEach
    public void setup() {
        MockitoAnnotations.openMocks(this);
    }

    static Stream<String> invalidAuthHeaders() {
        return Stream.of("invalid", null, "Bearer xxx");
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithoutSignatureValidation(String)}
     */
    @ParameterizedTest
    @MethodSource("invalidAuthHeaders")
    void testGetJwtWithoutSignatureValidation_whenInvalidAuthHeader_shouldReturnUnauthorizedException(String authHeader) {
        StepVerifier.create(authChecker.getJwtWithoutSignatureValidation(authHeader))
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }
    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithoutSignatureValidation(String)}
     */
    @Test
    void testGetJwtWithoutSignatureValidation_whenValidAuthHeader_shouldReturnSuccess() {
        // Arrange, Act and Assert
        String authHeader = "Bearer valid";
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        try (MockedStatic<SignedJWT> mockedStatic = Mockito.mockStatic(SignedJWT.class)) {
            mockedStatic.when(() -> SignedJWT.parse("valid")).thenReturn(mockedSignedJwt);


            StepVerifier.FirstStep<SignedJWT> createResult = StepVerifier
                    .create(authChecker.getJwtWithoutSignatureValidation(authHeader));
            createResult
                    .expectNext(mockedSignedJwt)
                    .verifyComplete();
        }
    }


    /**
     * Method under test: {@link AuthChecker#getClientID(SignedJWT)}
     */
    @Test
    void testGetClientID_whenNullJwt_shouldReturnExceptionUnauthorizedException() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<String> createResult =
                StepVerifier.create(authChecker.getClientID(null));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    /**
     * Method under test: {@link AuthChecker#getClientID(SignedJWT)}
     */
    @Test
    void testGetClientID_whenClaimsSetIsNull_shouldReturnExceptionUnauthorizedException() throws AssertionError, ParseException {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        when(jwt.getJWTClaimsSet()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<String> createResult =
                StepVerifier.create(authChecker.getClientID(jwt));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
        verify(jwt).getJWTClaimsSet();
    }

    /**
     * Method under test: {@link AuthChecker#getClientID(SignedJWT)}
     */
    @Test
    void testGetClientID_whenSubjectIsNull_shouldReturnExceptionUnauthorizedException() throws AssertionError, ParseException {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        JWTClaimsSet jwtClaimsSet = mock(JWTClaimsSet.class);
        when(jwt.getJWTClaimsSet()).thenReturn(jwtClaimsSet);
        when(jwtClaimsSet.getSubject()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<String> createResult =
                StepVerifier.create(authChecker.getClientID(jwt));
        createResult.expectError().verify();
        verify(jwt).getJWTClaimsSet();
    }

    /**
     * Method under test: {@link AuthChecker#getClientID(SignedJWT)}
     */
    @Test
    void testGetClientID_whenSubjectIsPresent_shouldReturnSubject() throws AssertionError, ParseException {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        JWTClaimsSet jwtClaimsSet = mock(JWTClaimsSet.class);
        when(jwt.getJWTClaimsSet()).thenReturn(jwtClaimsSet);
        when(jwtClaimsSet.getSubject()).thenReturn("subject");

        // Act and Assert
        StepVerifier.FirstStep<String> createResult =
                StepVerifier.create(authChecker.getClientID(jwt));
        createResult.expectNext("subject").verifyComplete();
        verify(jwt).getJWTClaimsSet();
    }

    /**
     * Method under test: {@link AuthChecker#getAlgorithm(SignedJWT)}
     */
    @Test
    void testGetAlgorithm_whenTokenIsNull_shouldReturnUnauthorizedException() {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<JWSAlgorithm> createResult =
                StepVerifier.create(authChecker.getAlgorithm(null));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    /**
     * Method under test: {@link AuthChecker#getAlgorithm(SignedJWT)}
     */
    @Test
    void testGetAlgorithm_whenTokenValid_shouldReturnAlgorithm() {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        String s = "foo";
        JWSAlgorithm alg = JWSAlgorithm.parse(s);
        when(jwt.getHeader()).thenReturn(new JWSHeader(alg));

        // Act and Assert
        StepVerifier.FirstStep<JWSAlgorithm> createResult =
                StepVerifier.create(authChecker.getAlgorithm(jwt));
        createResult.assertNext(j -> assertSame(alg, j)).expectComplete().verify();
        verify(jwt).getHeader();
    }

    /**
     * Method under test: {@link AuthChecker#getAlgorithm(SignedJWT)}
     */
    @Test
    void testGetAlgorithm_whenNullTokenHeader_shouldReturnUnauthorizedException() throws UnauthorizedException, AssertionError {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        when(jwt.getHeader()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<JWSAlgorithm> createResult =
                StepVerifier.create(authChecker.getAlgorithm(jwt));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
        verify(jwt).getHeader();
    }

    /**
     * Method under test:
     * {@link AuthChecker#deriveAudience(AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testDeriveAudience_whenValidServiceID_shouldReturnServiceIDInExpectedForm() {
        // Arrange
        AuthorizationInfo authInfo = mock(AuthorizationInfo.class);
        when(authProperties.getServiceId()).thenReturn("operator-service-id");

        // For Operator
        // Act
        Mono<String> result = authChecker.deriveAudience(authInfo, AuthRoles.OPERATOR);

        // Assert
        StepVerifier.create(result)
                .expectNext("s:operator-service-id")
                .verifyComplete();

        // For Tenant
        // Act
        when(authProperties.getServiceId()).thenReturn("tenant-service-id");
        result = authChecker.deriveAudience(authInfo, AuthRoles.TENANT_NOTARY);

        // Assert
        StepVerifier.create(result)
                .expectNext("tenant-service-id")
                .verifyComplete();


        // For Notary
        // Act
        when(authInfo.getIss()).thenReturn("https://tenant-notary-service-id");
        result = authChecker.deriveAudience(authInfo, AuthRoles.TENANT_SERVICE);

        // Assert
        StepVerifier.create(result)
                .expectNext("s:tenant-notary-service-id")
                .verifyComplete();
    }

    /**
     * Method under test:
     * {@link AuthChecker#deriveAudience(AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testDeriveAudience_whenIssIsNullForTenantService_shouldReturnUnauthorizedException() throws AssertionError {
        // Arrange, Act and Assert
        AuthorizationInfo authInfo = mock(AuthorizationInfo.class);
        when(authInfo.getIss()).thenReturn(null);
        StepVerifier.FirstStep<String> createResult = StepVerifier
                .create(authChecker.deriveAudience(authInfo,
                        AuthRoles.TENANT_SERVICE));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithSignatureValidation(SignedJWT, AuthorizationInfo)}
     */
    @Test
    void testGetJwtWithSignatureValidation_whenJwtIsNull_shouldReturnUnauthorizedException() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Jwt> createResult = StepVerifier
                .create(authChecker.getJwtWithSignatureValidation(null,
                        new AuthorizationInfo("42", "Name", "Iss", "Jwks")));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithSignatureValidation(SignedJWT, AuthorizationInfo)}
     */
    @Test
    void testGetJwtWithSignatureValidation_whenGetJwtFails_shouldReturnUnauthorizedException() throws AssertionError {
        // Arrange
        String testJwksURL = "testJwksURL";
        String testTokenString = "testTokenString";

        SignedJWT mockedJwt = mock(SignedJWT.class);
        JWSHeader mockedJWSHeader = mock(JWSHeader.class);
        JWSAlgorithm mockedJWSAlgorithm = mock(JWSAlgorithm.class);

        when(mockedJwt.getHeader()).thenReturn(mockedJWSHeader);
        when(mockedJWSHeader.getAlgorithm()).thenReturn(mockedJWSAlgorithm);
        when(mockedJwt.getParsedString()).thenReturn(testTokenString);
        when(jwtSignatureValidationService.getJwt(testJwksURL, mockedJWSAlgorithm, testTokenString)).thenReturn(Mono.error(new UnauthorizedException("decoding error")));
        // Act and Assert
        StepVerifier.FirstStep<Jwt> createResult = StepVerifier
                .create(authChecker.getJwtWithSignatureValidation(mockedJwt,
                        new AuthorizationInfo("42", "Name", "Iss", testJwksURL)));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
        verify(mockedJwt).getHeader();
        verify(mockedJWSHeader).getAlgorithm();
        verify(mockedJwt).getParsedString();
        verify(jwtSignatureValidationService).getJwt(testJwksURL, mockedJWSAlgorithm, testTokenString);
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithSignatureValidation(SignedJWT, AuthorizationInfo)}
     */
    @Test
    void testGetJwtWithSignatureValidation_whenGetJwtSucceeds_shouldReturnDecodedJwt() {
        // Arrange
        String testJwksURL = "testJwksURL";
        String testTokenString = "testTokenString";

        SignedJWT mockedJwt = mock(SignedJWT.class);
        JWSHeader mockedJWSHeader = mock(JWSHeader.class);
        JWSAlgorithm mockedJWSAlgorithm = mock(JWSAlgorithm.class);
        Jwt mockedDecodedJwt = mock(Jwt.class);

        when(mockedJwt.getHeader()).thenReturn(mockedJWSHeader);
        when(mockedJWSHeader.getAlgorithm()).thenReturn(mockedJWSAlgorithm);
        when(mockedJwt.getParsedString()).thenReturn(testTokenString);
        when(jwtSignatureValidationService.getJwt(testJwksURL, mockedJWSAlgorithm, testTokenString)).thenReturn(Mono.just(mockedDecodedJwt));
        // Act and Assert
        StepVerifier.FirstStep<Jwt> createResult = StepVerifier
                .create(authChecker.getJwtWithSignatureValidation(mockedJwt,
                        new AuthorizationInfo("42", "Name", "Iss", testJwksURL)));
        createResult
                .expectNext(mockedDecodedJwt)
                .verifyComplete();

        verify(mockedJwt).getHeader();
        verify(mockedJWSHeader).getAlgorithm();
        verify(mockedJwt).getParsedString();
        verify(jwtSignatureValidationService).getJwt(testJwksURL, mockedJWSAlgorithm, testTokenString);
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithSignatureValidation(SignedJWT, AuthorizationInfo)}
     */
    @Test
    void testGetJwtWithSignatureValidation_whenJwtHeaderIsNull_shouldReturnUnauthorizedException() throws AssertionError {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        when(jwt.getHeader()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<Jwt> createResult = StepVerifier
                .create(authChecker.getJwtWithSignatureValidation(jwt,
                        new AuthorizationInfo("42", "Name", "Iss", "Jwks")));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();

        verify(jwt).getHeader();
    }

    /**
     * Method under test:
     * {@link AuthChecker#getJwtWithSignatureValidation(SignedJWT, AuthorizationInfo)}
     */
    @Test
    void testGetJwtWithSignatureValidation_whenJwsAlgorithmIsNull_shouldReturnUnauthorizedException() throws AssertionError {
        // Arrange
        SignedJWT mockedJwt = mock(SignedJWT.class);
        JWSHeader mockedJWSheader = mock(JWSHeader.class);
        when(mockedJwt.getHeader()).thenReturn(mockedJWSheader);
        when(mockedJWSheader.getAlgorithm()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<Jwt> createResult = StepVerifier
                .create(authChecker.getJwtWithSignatureValidation(mockedJwt,
                        new AuthorizationInfo("42", "Name", "Iss", "Jwks")));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();

        verify(mockedJwt).getHeader();
        verify(mockedJWSheader).getAlgorithm();
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenGetJwtWithSignatureValidationFails_shouldReturnUnauthorizedException() throws AssertionError {

        // Arrange
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.error(new UnauthorizedException("failed to decode token")))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);
        // Act and Assert
        StepVerifier.create(authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                .expectErrorMatches(e -> e instanceof UnauthorizedException && e.getMessage().contains("failed to decode token"))
                .verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenValidateIssuerFails_shouldReturnUnauthorizedException() throws AssertionError {

        // Arrange
        Jwt mockedJwt = mock(Jwt.class);
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);
        JwtClaimValidator mockedValidator = mock(JwtClaimValidator.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedJwt))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);

        try (MockedStatic<JwtClaimValidator> mockedStatic = Mockito.mockStatic(JwtClaimValidator.class)) {
            // Mock and test static methods here
            mockedStatic.when(() -> JwtClaimValidator.JwtClaimValidatorWithJwt(mockedJwt))
                    .thenReturn(mockedValidator);

            when(mockedValidator.validateIssuer("some iss")).thenReturn(
                    Mono.error(new UnauthorizedException("failed to match issuer")));

            when(mockedAuthInfo.getIss()).thenReturn("some iss");
            // Act and Assert
            StepVerifier.create(
                            authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                    .expectErrorMatches(e -> e instanceof UnauthorizedException &&
                            e.getMessage().contains("failed to match issuer"))
                    .verify();
        }
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenDeriveAudFails_shouldReturnUnauthorizedException() throws AssertionError {

        // Arrange
        Jwt mockedJwt = mock(Jwt.class);
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);
        JwtClaimValidator mockedValidator = mock(JwtClaimValidator.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedJwt))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);

        doReturn(Mono.error(new UnauthorizedException("some error in deriveAudience")))
                .when(authCheckerSpy).deriveAudience(mockedAuthInfo, AuthRoles.OPERATOR);

        try (MockedStatic<JwtClaimValidator> mockedStatic = Mockito.mockStatic(JwtClaimValidator.class)) {
            // Mock and test static methods here
            mockedStatic.when(() -> JwtClaimValidator.JwtClaimValidatorWithJwt(mockedJwt))
                    .thenReturn(mockedValidator);

            when(mockedValidator.validateIssuer("some iss")).thenReturn(Mono.just(mockedValidator));
            when(mockedAuthInfo.getIss()).thenReturn("some iss");
            // Act and Assert
            StepVerifier.create(
                            authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                    .expectErrorMatches(e -> e instanceof UnauthorizedException &&
                            e.getMessage().contains("some error in deriveAudience"))
                    .verify();
        }
    }


    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenValidateAudFails_shouldReturnUnauthorizedException() throws AssertionError {

        // Arrange
        Jwt mockedJwt = mock(Jwt.class);
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);
        JwtClaimValidator mockedValidator = mock(JwtClaimValidator.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedJwt))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);

        doReturn(Mono.just("some aud"))
                .when(authCheckerSpy).deriveAudience(mockedAuthInfo, AuthRoles.OPERATOR);

        try (MockedStatic<JwtClaimValidator> mockedStatic = Mockito.mockStatic(JwtClaimValidator.class)) {
            // Mock and test static methods here
            mockedStatic.when(() -> JwtClaimValidator.JwtClaimValidatorWithJwt(mockedJwt))
                    .thenReturn(mockedValidator);

            when(mockedValidator.validateIssuer("some iss")).thenReturn(Mono.just(mockedValidator));
            when(mockedAuthInfo.getIss()).thenReturn("some iss");

            when(mockedValidator.validateAud("some aud")).thenReturn(Mono.error(new UnauthorizedException("mis-matched aud")));

            // Act and Assert
            StepVerifier.create(
                            authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                    .expectErrorMatches(e -> e instanceof UnauthorizedException &&
                            e.getMessage().contains("mis-matched aud"))
                    .verify();
        }
    }


    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenValidateSubjectFails_shouldReturnUnauthorizedException() throws AssertionError {

        // Arrange
        Jwt mockedJwt = mock(Jwt.class);
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);
        JwtClaimValidator mockedValidator = mock(JwtClaimValidator.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedJwt))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);

        doReturn(Mono.just("some aud"))
                .when(authCheckerSpy).deriveAudience(mockedAuthInfo, AuthRoles.OPERATOR);

        try (MockedStatic<JwtClaimValidator> mockedStatic = Mockito.mockStatic(JwtClaimValidator.class)) {
            // Mock and test static methods here
            mockedStatic.when(() -> JwtClaimValidator.JwtClaimValidatorWithJwt(mockedJwt))
                    .thenReturn(mockedValidator);

            when(mockedValidator.validateIssuer("some iss")).thenReturn(Mono.just(mockedValidator));
            when(mockedAuthInfo.getIss()).thenReturn("some iss");
            when(mockedValidator.validateAud("some aud")).thenReturn(Mono.just(mockedValidator));
            when(mockedValidator.validateSubject("some sub")).thenReturn(Mono.error(new UnauthorizedException("mis-matched sub")));

            when(mockedAuthInfo.getId()).thenReturn("some sub");
            // Act and Assert
            StepVerifier.create(
                            authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                    .expectErrorMatches(e -> e instanceof UnauthorizedException &&
                            e.getMessage().contains("mis-matched sub"))
                    .verify();
        }
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT_whenValidToken_shouldReturnSuccess() throws AssertionError {

        // Arrange
        Jwt mockedJwt = mock(Jwt.class);
        SignedJWT mockedSignedJwt = mock(SignedJWT.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);
        JwtClaimValidator mockedValidator = mock(JwtClaimValidator.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedJwt))
                .when(authCheckerSpy).getJwtWithSignatureValidation(mockedSignedJwt, mockedAuthInfo);

        doReturn(Mono.just("some aud"))
                .when(authCheckerSpy).deriveAudience(mockedAuthInfo, AuthRoles.OPERATOR);

        try (MockedStatic<JwtClaimValidator> mockedStatic = Mockito.mockStatic(JwtClaimValidator.class)) {
            mockedStatic.when(() -> JwtClaimValidator.JwtClaimValidatorWithJwt(mockedJwt))
                    .thenReturn(mockedValidator);

            when(mockedValidator.validateIssuer("some iss")).thenReturn(Mono.just(mockedValidator));
            when(mockedAuthInfo.getIss()).thenReturn("some iss");
            when(mockedValidator.validateAud("some aud")).thenReturn(Mono.just(mockedValidator));
            when(mockedValidator.validateSubject("some sub")).thenReturn(Mono.just(mockedValidator));

            when(mockedAuthInfo.getId()).thenReturn("some sub");
            // Act and Assert
            StepVerifier.create(
                            authCheckerSpy.validateJWT(mockedSignedJwt, mockedAuthInfo, AuthRoles.OPERATOR))
                    .expectNext(mockedValidator)
                    .verifyComplete();
        }
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT2() throws AssertionError {

        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        when(jwt.getHeader()).thenReturn(new JWSHeader(JWSAlgorithm.parse("foo")));

        // Act and Assert
        StepVerifier.FirstStep<JwtClaimValidator> createResult = StepVerifier
                .create(authChecker.validateJWT(jwt,
                        new AuthorizationInfo("42", "Name", "Iss", "Jwks"), AuthRoles.OPERATOR));
        createResult.expectError().verify();
        verify(jwt).getHeader();
    }

    /**
     * Method under test:
     * {@link AuthChecker#validateJWT(SignedJWT, AuthorizationInfo, AuthRoles)}
     */
    @Test
    void testValidateJWT3() throws AssertionError {
        // Arrange
        SignedJWT jwt = mock(SignedJWT.class);
        when(jwt.getHeader()).thenReturn(null);

        // Act and Assert
        StepVerifier.FirstStep<JwtClaimValidator> createResult = StepVerifier
                .create(authChecker.validateJWT(jwt,
                        new AuthorizationInfo("42", "Name", "Iss", "Jwks"), AuthRoles.OPERATOR));
        createResult.expectError().verify();
        verify(jwt).getHeader();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authenticateHelper(String, String, boolean, AuthRoles)}
     */
    @Test
    void testAuthenticateHelper() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<AuthChecker.AuthContext> createResult = StepVerifier
                .create(authChecker.authenticateHelper("Namespace", "Auth Header",
                        false, AuthRoles.OPERATOR));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authenticateHelper(String, String, boolean, AuthRoles)}
     */
    @Test
    void testAuthenticateHelper2() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<AuthChecker.AuthContext> createResult =
                StepVerifier.create(authChecker.authenticateHelper(
                        "Namespace", "com.nvidia.ess.auth.AuthChecker", false,
                        AuthRoles.OPERATOR));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authenticateHelper(String, String, boolean, AuthRoles)}
     */

    @Test
    void authenticateHelper_whenBadJwt_shouldReturnError() throws AssertionError {
        // Arrange, Act and Assert
        String authHeader = "Bearer xx";
        AuthChecker authCheckerSpy = Mockito.spy(authChecker);
        when(authCheckerSpy.getJwtWithoutSignatureValidation(authHeader)).thenReturn(Mono.error(() -> new UnauthorizedException("jwt does not contain client id")));
        StepVerifier.FirstStep<AuthChecker.AuthContext> createResult =
                StepVerifier
                .create(authCheckerSpy.authenticateHelper("Namespace", authHeader, false, AuthRoles.OPERATOR));
        createResult
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    @Test
    void authenticateHelper_whenNoClientID_shouldReturnError() throws AssertionError {

        // Arrange
        String authHeader = "Bearer xx";
        String namespace = "ns";
        boolean isNotary = false;
        AuthRoles role = AuthRoles.OPERATOR;

        SignedJWT mockedSignedJwt = mock(SignedJWT.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedSignedJwt)).when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);
        doReturn(Mono.error(new UnauthorizedException("jwt does not contain client id")))
                .when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);

        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role))
                .expectError(UnauthorizedException.class)
                .verify();
    }
    @Test
    void authenticateHelper_whenAuthNotFound_shouldReturnForbiddenException() {
        // Arrange
        String authHeader = "Bearer xx";
        String clientID = "clientID";
        String namespace = "ns";
        boolean isNotary = false;
        AuthRoles role = AuthRoles.OPERATOR;

        SignedJWT mockedSignedJwt = mock(SignedJWT.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedSignedJwt)).when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);
        doReturn(Mono.just(clientID)).when(authCheckerSpy).getClientID(mockedSignedJwt);
        when(authorizationsFacade.getAuthorization(namespace, isNotary, clientID))
                .thenReturn(Mono.error(new NotFoundException("clientID not found")));

        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role))
                .expectError(ForbiddenException.class)
                .verify();
    }

    @Test
    void authenticateHelper_whenUpstreamError_shouldReturnUpstreamException() {
        // Arrange
        String authHeader = "Bearer xx";
        String clientID = "clientID";
        String namespace = "ns";
        boolean isNotary = false;
        AuthRoles role = AuthRoles.OPERATOR;

        SignedJWT mockedSignedJwt = mock(SignedJWT.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        doReturn(Mono.just(mockedSignedJwt)).when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);
        doReturn(Mono.just(clientID)).when(authCheckerSpy).getClientID(mockedSignedJwt);
        when(authorizationsFacade.getAuthorization(namespace, isNotary, clientID))
                .thenReturn(Mono.error(new UpstreamException("Upstream error")));

        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role))
                .expectError(UpstreamException.class)
                .verify();
    }

    @Test
    void authenticateHelper_whenValidateJWTError_shouldReturnNotAuthorized() {
        // Arrange
        String authHeader = "Bearer xx";
        String clientID = "clientID";
        String namespace = "ns";
        boolean isNotary = false;
        AuthRoles role = AuthRoles.OPERATOR;

        SignedJWT mockedSignedJwt = mock(SignedJWT.class);

        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        // Mock ServerWebExchange
        ServerWebExchange mockExchange = mock(ServerWebExchange.class);

        doReturn(Mono.just(mockedSignedJwt)).when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);
        doReturn(Mono.just(clientID)).when(authCheckerSpy).getClientID(mockedSignedJwt);
        when(authorizationsFacade.getAuthorization(namespace, isNotary, clientID))
                .thenReturn(Mono.just(mockedAuthInfo));

        doReturn(Mono.error(new UnauthorizedException("not authorized error")))
                .when(authCheckerSpy)
                .validateJWT(mockedSignedJwt, mockedAuthInfo, role);
        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role)
                        .contextWrite(Context.of(ServerWebExchange.class, mockExchange))) // Set context
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();


        doReturn(Mono.error(new ForbiddenException("forbidden error")))
                .when(authCheckerSpy)
                .validateJWT(mockedSignedJwt, mockedAuthInfo, role);
        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role)
                        .contextWrite(Context.of(ServerWebExchange.class, mockExchange))) // Set context
                .expectErrorMatches(ForbiddenException.class::isInstance)
                .verify();


        doReturn(Mono.error(new UnauthorizedException("jwt validation error")))
                .when(authCheckerSpy)
                .validateJWT(mockedSignedJwt, mockedAuthInfo, role);
        // Act and Assert
        StepVerifier.create(authCheckerSpy.authenticateHelper(namespace, authHeader, isNotary, role)
                        .contextWrite(Context.of(ServerWebExchange.class, mockExchange))) // Set context
                .expectErrorMatches(UnauthorizedException.class::isInstance)
                .verify();
    }

    /**
     * Method under test: {@link AuthChecker#authOperator(String, String[])}
     */
    @Test
    void testAuthOperator() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authOperator("Auth Header", new String[] {"Ess Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test: {@link AuthChecker#authOperator(String, String[])}
     */
    @Test
    void testAuthOperator2() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authOperator("com.nvidia.ess.auth.AuthChecker",
                        new String[] {"Ess Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test: {@link AuthChecker#authTenant(String, String, String[])}
     */
    @Test
    void testAuthTenant() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authTenant("Namespace", "Auth Header",
                        new String[] {"Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test: {@link AuthChecker#authTenant(String, String, String[])}
     */
    @Test
    void testAuthTenant2() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier.create(
                authChecker.authTenant("Namespace", "com.nvidia.ess.auth.AuthChecker",
                        new String[] {"Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authTenant(String, String, String[], String)}
     */
    @Test
    void testAuthTenant3() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier.create(
                authChecker.authTenant("Namespace", "Auth Header", new String[] {"Auth Scopes"},
                        "Client IDBeing Removed"));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authTenant(String, String, String[], String)}
     */
    @Test
    void testAuthTenant4() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult =
                StepVerifier.create(authChecker.authTenant("Namespace",
                        "com.nvidia.ess.auth.AuthChecker", new String[] {"Auth Scopes"},
                        "Client IDBeing Removed"));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authOperatorOrTenant(String, String, String[], String[])}
     */
    @Test
    void testAuthOperatorOrTenant() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult =
                StepVerifier.create(authChecker.authOperatorOrTenant("Namespace",
                        "Auth Header", new String[] {"Operator Auth Scopes"},
                        new String[] {"Tenant Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authOperatorOrTenant(String, String, String[], String[])}
     */
    @Test
    void testAuthOperatorOrTenant2() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult =
                StepVerifier.create(authChecker.authOperatorOrTenant("Namespace",
                        null, new String[] {"Operator Auth Scopes"},
                        new String[] {"Tenant Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authOperatorOrTenant(String, String, String[], String[])}
     */
    @Test
    void testAuthOperatorOrTenant3() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authOperatorOrTenant("Namespace",
                        "com.nvidia.ess.auth.AuthChecker",
                        new String[] {"Operator Auth Scopes"},
                        new String[] {"Tenant Auth Scopes"}));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authNotaryClient(String, String, String)}
     */
    @Test
    void testAuthNotaryClient() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authNotaryClient("Namespace", "Auth Header", "Secret Path"));
        createResult.expectError().verify();
    }

    /**
     * Method under test:
     * {@link AuthChecker#authNotaryClient(String, String, String)}
     */
    @Test
    void testAuthNotaryClient2() throws AssertionError {
        // Arrange, Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier
                .create(authChecker.authNotaryClient("Namespace",
                        "com.nvidia.ess.auth.AuthChecker", "Secret Path"));
        createResult.expectError().verify();
    }

    @Test
    void authOperator_shouldThrowForbiddenExceptionForOperator() throws ParseException {
        String[] essAuthScopes = new String[]{"scope1"};

        // Arrange
        SignedJWT mockedSignedJWT = mock(SignedJWT.class);
        JWTClaimsSet mockedClaimsSet = mock(JWTClaimsSet.class);
        AuthorizationInfo mockedAuthInfo = mock(AuthorizationInfo.class);

        when(mockedSignedJWT.getJWTClaimsSet()).thenReturn(mockedClaimsSet);
        String clientID = "clientID";
        when(mockedClaimsSet.getSubject()).thenReturn(clientID);

        AuthChecker authCheckerSpy = Mockito.spy(authChecker);

        String authHeader = "Bearer token";
        doReturn(Mono.just(mockedSignedJWT)).when(authCheckerSpy).getJwtWithoutSignatureValidation(authHeader);
        doReturn(Mono.just(clientID)).when(authCheckerSpy).getClientID(mockedSignedJWT);
        String namespace = "namespace";
        when(authorizationsFacade.getAuthorization(eq(namespace), anyBoolean(), eq(clientID))).thenReturn(Mono.just(mockedAuthInfo));

        // Mock validateJWT to throw a ForbiddenException
        doReturn(Mono.error(new ForbiddenException("forbidden")))
                .when(authCheckerSpy)
                .validateJWT(eq(mockedSignedJWT), eq(mockedAuthInfo), any());

        // Act and Assert
        StepVerifier.FirstStep<Boolean> createResult = StepVerifier.create(authCheckerSpy.authOperator(authHeader, essAuthScopes, false));
        createResult.expectErrorMatches(ForbiddenException.class::isInstance).verify();
    }
}
