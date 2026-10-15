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

package com.nvidia.boot.audit;

import static java.lang.String.format;

import tools.jackson.databind.JsonNode;
import java.net.NetworkInterface;
import java.net.SocketException;
import java.nio.ByteBuffer;
import java.security.InvalidKeyException;
import java.security.NoSuchAlgorithmException;
import java.security.NoSuchProviderException;
import java.security.Security;
import java.util.Base64;
import java.util.Enumeration;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.ArrayUtils;
import org.apache.commons.lang3.StringUtils;
import org.bouncycastle.jce.provider.BouncyCastleProvider;

/**
 * Audit helpers: machine identifier and HMAC formatting uses (algorithm, canonicalization via
 * {@link JsonNode#hashCode()}, and the {@code alg:kid:base64(hmac)} string format) as per
 * NVIDIA Audit Specification.
 */
@Slf4j
public final class AuditUtils {

    private static final String HMAC_FORMAT = "%s:%s:%s";
    private static final String ALGORITHM_HMAC_SHA3_512 = "HMac-SHA3-512";

    private static final String MESG_MAC_ADDRESS =
            "Failed to obtain MAC address";
    private static final String MESG_INVALID_HMAC_KID =
            "Invalid HMAC kid. HMAC kid cannot be empty";
    private static final String MESG_INVALID_HMAC_KEY =
            "Invalid HMAC key. HMAC key cannot be empty";

    static {
        if (Security.getProvider("BC") == null) {
            Security.addProvider(new BouncyCastleProvider());
        }
    }

    private AuditUtils() {}

    /**
     * Returns the first hardware MAC address found on the host, formatted as hex pairs separated
     * by {@code '-'}.
     */
    public static String getMacAddress() {
        try {
            byte[] macAddress = null;
            Enumeration<NetworkInterface> nwInterface = NetworkInterface.getNetworkInterfaces();
            while (nwInterface.hasMoreElements()) {
                NetworkInterface nis = nwInterface.nextElement();
                if (nis != null) {
                    macAddress = nis.getHardwareAddress();
                    if (macAddress != null) {
                        break;
                    }
                }
            }
            if (macAddress == null) {
                log.error(MESG_MAC_ADDRESS);
                throw new IllegalStateException(MESG_MAC_ADDRESS);
            }
            var stringBuilder = new StringBuilder();
            for (int i = 0; i < macAddress.length; i++) {
                if (i != 0) {
                    stringBuilder.append("-");
                }
                String s = Integer.toHexString(macAddress[i] & 0xFF);
                stringBuilder.append((s.length() == 1) ? "0" + s : s);
            }
            return stringBuilder.toString();
        } catch (SocketException e) {
            log.error(MESG_MAC_ADDRESS);
            throw new RuntimeException(MESG_MAC_ADDRESS, e);
        }
    }

    /**
     * Returns {@code alg:kid:base64(hmac)} using HMAC-SHA3-512 over the four big-endian bytes of
     * {@code jsonNode.hashCode()}.
     */
    public static String computeHmacFormatted(
            @NonNull String kid,
            @NonNull byte[] key,
            @NonNull JsonNode jsonNode) {
        if (StringUtils.isBlank(kid)) {
            log.error(MESG_INVALID_HMAC_KID);
            throw new IllegalArgumentException(MESG_INVALID_HMAC_KID);
        }
        if (ArrayUtils.isEmpty(key)) {
            log.error(MESG_INVALID_HMAC_KEY);
            throw new IllegalArgumentException(MESG_INVALID_HMAC_KEY);
        }
        try {
            Mac mac = Mac.getInstance(ALGORITHM_HMAC_SHA3_512, "BC");
            SecretKeySpec secretKeySpec = new SecretKeySpec(key, ALGORITHM_HMAC_SHA3_512);
            mac.init(secretKeySpec);

            // Using two JSON strings, with identical property names and values but in different
            // order, directly to compute HMAC will result in two different hash values. From
            // JSON standpoint, the string representation may seem identical. However, when the
            // strings are used to compute HMAC, they result in different hash values. To address
            // this issue, we use Jackson's JsonNode.hashCode() to compute HMAC as it will always
            // return the same value regardless of the property order. Jackson implements equals()
            // and hashCode() correctly(that is, sticking to the contract established by
            // java.lang.Object), for all JSON values (numbers, booleans, strings, objects
            // recursively, arrays recursively, nulls).
            byte[] jsonHashcode = ByteBuffer.allocate(4).putInt(jsonNode.hashCode()).array();
            byte[] hmac = mac.doFinal(jsonHashcode);
            String encodedHmac = Base64.getEncoder().encodeToString(hmac);
            return format(HMAC_FORMAT, ALGORITHM_HMAC_SHA3_512, kid, encodedHmac);
        } catch (NoSuchAlgorithmException | InvalidKeyException | NoSuchProviderException e) {
            var mesg = "Failed to compute HMAC";
            log.error(mesg, e);
            throw new IllegalArgumentException(mesg, e);
        }
    }
}
