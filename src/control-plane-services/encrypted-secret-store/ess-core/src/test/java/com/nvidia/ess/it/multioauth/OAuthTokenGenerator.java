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
package com.nvidia.ess.it.multioauth;

import static java.time.temporal.ChronoUnit.SECONDS;
import static org.springframework.security.oauth2.core.oidc.IdTokenClaimNames.AZP;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.it.IntegrationTestProperties.OAuth2ClientProperties;
import com.nvidia.ess.util.PrintUtils;
import jakarta.annotation.Nullable;
import java.net.URL;
import java.security.KeyPairGenerator;
import java.security.interfaces.ECPrivateKey;
import java.security.interfaces.ECPublicKey;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

@Slf4j
public class OAuthTokenGenerator {

    private final JWSSigner signer;

    @Getter
    private final JWKSet jwks;

    private final Map<String, OAuth2ClientProperties> clientIdToClientPropertyMap;

    public OAuthTokenGenerator(List<OAuth2ClientProperties> clients) {
        try {
            clientIdToClientPropertyMap = clients.stream().collect(Collectors.toMap(OAuth2ClientProperties::getSub,
                    Function.identity()));

            var gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(Curve.P_256.toECParameterSpec());
            var keyPair = gen.generateKeyPair();

            // Convert to JWK format
            var privateJwk = new ECKey.Builder(Curve.P_256,
                                               (ECPublicKey) keyPair.getPublic())
                    .privateKey((ECPrivateKey) keyPair.getPrivate())
                    .keyID(UUID.randomUUID().toString())
                    .algorithm(JWSAlgorithm.ES256)
                    .build();
            var publicJWK = privateJwk.toPublicJWK();
            jwks = new JWKSet(publicJWK);
            signer = new ECDSASigner(privateJwk);
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    public String getJwt(String sub, String secret, List<String> scopes, int expireInSeconds, URL issuer,
            String options) throws UnauthorizedException, ForbiddenException {

        validateSubAndSecret(sub, secret);
        return getJwt(sub, scopes, expireInSeconds, issuer, options, false);
    }

    public String getJwt(String subject, List<String> scopes, int expireInSeconds, URL issuer, String options,
            boolean skipClaimValidation) throws UnauthorizedException, ForbiddenException {
        return getJwt(signer, jwks.toPublicJWKSet(), subject, scopes, expireInSeconds, issuer, options,
                skipClaimValidation);
    }

    @SuppressWarnings("unchecked")
    @SneakyThrows(JOSEException.class)
    public String getJwt(JWSSigner signer, JWKSet jwks, JWTClaimsSet.Builder claims, boolean skipClaimValidation)
            throws UnauthorizedException, ForbiddenException {
        var builtClaims = claims.build();

        if (!skipClaimValidation) {
            validateSubAndScopes(builtClaims.getSubject(), (List<String>) builtClaims.getClaim("scopes"));
        }

        var jwk = jwks.getKeys().get(0);
        SignedJWT signedJWT = new SignedJWT(
                new JWSHeader.Builder(new JWSAlgorithm(jwk.getAlgorithm().getName()))
                        .keyID(jwk.getKeyID())
                        .build(),
                builtClaims);
        signedJWT.sign(signer);
        var signedJWTString = signedJWT.serialize();
        log.debug("Returning signed JWT for claims: {} . Token: {}", builtClaims.toString(),
                PrintUtils.signedJWTToString(signedJWTString));
        return signedJWTString;
    }

    public String getJwt(JWSSigner signer, JWKSet jwks, String subject, List<String> scopes, int expireInSeconds, URL issuer,
            String options, boolean skipClaimValidation) throws UnauthorizedException, ForbiddenException {

        if (scopes == null) {
            scopes = List.of();
        }

        Date now = new Date();
        var claimsSetBuilder = new JWTClaimsSet.Builder()
                .subject(subject)
                .issueTime(now)
                .expirationTime(Date.from(now.toInstant().plus(expireInSeconds, SECONDS)))
                .claim("scopes", scopes)
                .audience(List.of(getServiceId(issuer), subject))
                .issuer(issuer.toString())
                .claim(AZP, subject);

        if (StringUtils.isNotBlank(options)) {
            claimsSetBuilder.claim("options", options);
        }
        return getJwt(signer, jwks, claimsSetBuilder, skipClaimValidation);
    }

    private void validateSubAndScopes(String sub, @Nullable List<String> scopes) throws UnauthorizedException,
            ForbiddenException {

        var client = clientIdToClientPropertyMap.get(sub);
        if (client == null) {
            throw new UnauthorizedException("Unauthorized OAuth2 service-client: " + sub);
        }
        if (!Objects.isNull(scopes) &&
                !scopes.stream().allMatch(claimedScope -> client.getScopes().contains(claimedScope))) {
            throw new ForbiddenException("OAuth2 service-client: " + sub + " cannot claim at least "
                    + "one of these scopes: " + String.join(", ", scopes));
        }
    }

    private void validateSubAndSecret(String sub, String secret) throws UnauthorizedException {

        var client = clientIdToClientPropertyMap.get(sub);
        if (client == null || !Objects.equals(secret, client.getSecret())) {
            throw new UnauthorizedException("Unauthorized OAuth2 service-client: " + sub);
        }
    }
    private static String getServiceId(URL issuer) {
        if (issuer == null) {
            throw new IllegalArgumentException("Issuer URL cannot be null");
        }
        var host = issuer.getHost();
        if (host == null || host.isEmpty()) {
            throw new IllegalArgumentException("Issuer URL must have a valid host");
        }
        var index = host.indexOf('.');
        var endIndex = (index != -1) ? index : host.length();
        return "s:" + host.substring(0, endIndex);
    }
}
