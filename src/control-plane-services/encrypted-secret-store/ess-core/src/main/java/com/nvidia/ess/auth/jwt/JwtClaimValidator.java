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
package com.nvidia.ess.auth.jwt;

import static com.nvidia.ess.constants.Constants.REDACTED;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.auth.NotaryProperties;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.utils.DateUtils;
import java.net.URL;
import java.time.Instant;
import java.util.Arrays;
import java.util.Date;
import java.util.List;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.oauth2.jwt.Jwt;
import reactor.core.publisher.Mono;

/*
This class is used when signature of JWT is required to be validated with reactive API
This class will make sure that public key is download from jwksURL and once validated it returns
 */
@Slf4j
public class JwtClaimValidator {

    private final Jwt jwt;

    public JwtClaimValidator(Jwt jwt) {
        this.jwt = jwt;
    }

    public static JwtClaimValidator JwtClaimValidatorWithJwt(Jwt jwt) {
       return(new JwtClaimValidator(jwt));
    }

    public Mono<JwtClaimValidator> validateIssuer(String issuer) {
        URL issuerInToken = jwt.getIssuer();
        // TODO match with exact rather than contains....
        if (issuerInToken == null || !issuer.contains(issuerInToken.getAuthority())) {
            String errMsg = String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, "iss", issuer, "iss", issuerInToken);
            log.error(errMsg);
            return Mono.error(() -> new UnauthorizedException(errMsg));
        }
        return Mono.just(this);
    }

    public Mono<JwtClaimValidator> validateSubject(String sub) {
        String subInToken = jwt.getSubject();
        if (subInToken == null || !subInToken.equals(sub)) {
            log.error(String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, "sub", sub, "sub",
                    subInToken));
            return Mono.error(() -> new UnauthorizedException(
                    String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, "sub", REDACTED, "sub",
                            subInToken)));
        }
        return Mono.just(this);
    }

    public Mono<JwtClaimValidator> validateAud(String aud) {
        List<String> audListInToken = jwt.getAudience();
        for (String audInToken : audListInToken) {
            if (audInToken.equals(aud)) {
                return Mono.just(this);
            }
        }
        String errMsg = String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, "aud", aud, "aud", audListInToken);
        log.error(errMsg);
        return Mono.error(() -> new UnauthorizedException(errMsg));
    }

    public Mono<JwtClaimValidator> validateScopes(String[] scopes) {
        List<String> claimScopes = null;
        String SCOPES = "scopes";
        if (jwt.getClaims().containsKey(SCOPES)) {
            claimScopes = jwt.getClaimAsStringList(SCOPES);
        }

        boolean found = false;

        if (claimScopes != null) {
            for (String authScope : scopes) {
                if (claimScopes.contains(authScope)) {
                    found = true;
                    break;
                }
            }
        }

        if (!found) {
            String errMsg = String.format(Constants.MSG_INSUFFICIENT_SCOPES, Arrays.toString(scopes));
            log.error(errMsg);
            return Mono.error(() -> new ForbiddenException(errMsg));
        }

        return Mono.just(this);

    }

    public Mono<JwtClaimValidator> validateAssertions(String namespace, String secretPath,
                                                      NotaryProperties notaryProperties) {

        try {
            // validate Iat
            Instant issueTime = jwt.getIssuedAt();
            if (issueTime == null) {
                log.error(Constants.MSG_MISSING_IAT);
                return Mono.error(
                        () -> new UnauthorizedException(Constants.MSG_MISSING_IAT));
            }

            long issuedAtTime = issueTime.toEpochMilli();
            long currentEpochTime = new Date().getTime();
            // expiry is one hour after issue time
            long expiryTime = issuedAtTime +
                    notaryProperties.getTtl().toMillis() +
                    notaryProperties.getClockSkewAdjustments().toMillis();
            if (expiryTime < currentEpochTime) {
                String errMsg = String.format(Constants.MSG_TOKEN_EXPIRED,
                        DateUtils.epochToDateString(expiryTime));
                log.error(errMsg);
                return Mono.error(() -> new UnauthorizedException(errMsg));
            }

            // validate Assertion key
            Map<String, Object> verifiedAssertions = null;
            String ASSERTION = "assertion";
            if (jwt.getClaims().containsKey(ASSERTION)) {
                verifiedAssertions = jwt.getClaimAsMap(ASSERTION);
            }

            // validate Assertion namespace
            String NAMESPACE = "namespace";
            if (verifiedAssertions == null || !verifiedAssertions.containsKey(NAMESPACE) ||
                    !namespace.equals(verifiedAssertions.get(NAMESPACE))) {
                String errMsg =
                        String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, ASSERTION + "." + NAMESPACE, namespace, ASSERTION, verifiedAssertions);
                log.error(errMsg);
                return Mono.error(() -> new ForbiddenException(errMsg));
            }

            // validate Assertion secret paths

            String SECRET_PATHS = "secretPaths";
            if (!verifiedAssertions.containsKey(SECRET_PATHS)) {
                String errMsg = String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, ASSERTION + "." + SECRET_PATHS,
                        secretPath, ASSERTION, verifiedAssertions);
                return Mono.error(() -> new ForbiddenException(errMsg));
            }

            Object claimSecretPathsObject = verifiedAssertions.get(SECRET_PATHS);

            if (!(claimSecretPathsObject instanceof List)) {
                String errMsg = String.format(Constants.MSG_INVALID_ASSERTIONS, verifiedAssertions);
                return Mono.error(
                        () -> new ForbiddenException(errMsg));
            }

            List<String> claimSecretPaths = (List<String>) claimSecretPathsObject;

            boolean found = false;

            for (String claimSecretPath : claimSecretPaths) {
                if (claimSecretPath.equals(secretPath)) {
                    found = true;
                    break;
                }
            }

            if (!found) {
                String errMsg = String.format(Constants.MSG_FAILED_TO_MATCH_CLAIM, ASSERTION + "." + SECRET_PATHS,
                        secretPath, ASSERTION + "." + SECRET_PATHS, claimSecretPaths);
                log.error(errMsg);
                return Mono.error(() -> new ForbiddenException(errMsg));
            }

            return Mono.just(this);

        } catch (Exception e) {
            log.error("notary token validation failed", e);
            return Mono.error(() -> new ForbiddenException(e.getMessage()));
        }
    }
}