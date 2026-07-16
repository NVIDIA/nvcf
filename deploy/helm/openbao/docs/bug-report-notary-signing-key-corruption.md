# Bug Report: Intermittent Notary Signing Key Corruption

> **Status**: ✅ Root Cause Identified, Fix Applied, **Fix Validated**
> **Severity**: High
> **Affected Component**: `service-migrations/migrations/utils/encryption_setup.sh`
> **Affected Service**: `nvcf-notary` (JWT signing)
> **Date Identified**: December 2024
> **Date Validated**: December 2024

---

## 1. Executive Summary

An intermittent bug in the EC P-256 signing key generation function causes approximately **5.6% of generated keys to be corrupted** (higher than initially estimated). When affected, the notary service produces JWTs with invalid signatures that fail verification by downstream services.

**Key Finding**: The bug only manifests on **Linux** (production environment) due to differences in how `xxd` handles invalid characters. macOS `xxd` behaves differently, making local testing unreliable without containerization.

---

## 2. Symptoms

- JWT signature verification fails on jwt.io and in consuming services
- The JWK's `d` (private key) parameter is 42 base64url characters instead of the required 43
- Occurs randomly on new cluster deployments
- Once a corrupted key is generated, all JWTs signed by that key are invalid

---

## 3. Root Cause Analysis

### 3.1 The Bug Location

**File**: `service-migrations/migrations/utils/encryption_setup.sh`
**Function**: `generate_asymmetric_signing_key()`
**Lines**: 112-149

### 3.2 The Flawed Code

```bash
# Line 112-115: Extract private key value
D_VALUE=$(openssl ec -in "$TEMP_KEY" -text -noout 2>/dev/null | \
    awk '/priv:/{flag=1; next} /pub:/{flag=0} flag{print}' | \
    tr -d ' \n:' | \
    sed 's/^00*//')    # <-- PROBLEM 1: Strips leading zeros

# Line 145-146: Pad to 64 hex characters
D_PADDED=$(printf "%064s" "$D_VALUE")  # <-- PROBLEM 2: Pads with spaces, not zeros
```

### 3.3 Problem 1: Leading Zero Stripping

The `sed 's/^00*//'` command removes leading zeros from the hex representation of the private key. This is incorrect because:

- EC private keys are fixed-length 256-bit integers
- When the integer's high bytes are `0x00`, they MUST be preserved
- Stripping them changes the mathematical value of the key

**Example:**
```
Original hex:    00a1b2c3d4...  (64 chars, starts with 00)
After sed:       a1b2c3d4...    (62 chars, leading 00 removed)
```

### 3.4 Problem 2: Space Padding Instead of Zero Padding

The `printf "%064s"` format specifier pads strings with **spaces** on the left, not zeros:

```bash
$ printf "%064s" "abc"
                                                             abc
#                                                            ^^^
# 61 spaces followed by "abc" - NOT valid hex!
```

When this space-padded string is passed to `hex_to_base64url()`:

```bash
hex_to_base64url() {
    echo -n "$hex_input" | xxd -r -p | base64 | tr '+/' '-_' | tr -d '='
}
```

The `xxd -r -p` command silently ignores the invalid space characters, producing a shorter binary output than expected.

### 3.5 The Chain of Failure

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  1. OpenSSL generates EC private key with leading zero byte (1/256 chance) │
├─────────────────────────────────────────────────────────────────────────────┤
│  2. sed strips "00" prefix: "00abc..." → "abc..." (62 chars)               │
├─────────────────────────────────────────────────────────────────────────────┤
│  3. printf pads with spaces: "  abc..." (2 spaces + 62 chars)              │
├─────────────────────────────────────────────────────────────────────────────┤
│  4. xxd ignores spaces, converts only 62 hex chars → 31 bytes              │
├─────────────────────────────────────────────────────────────────────────────┤
│  5. base64url of 31 bytes = 42 chars (should be 43 for 32 bytes)           │
├─────────────────────────────────────────────────────────────────────────────┤
│  6. JWK stored with corrupted 'd' value                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│  7. All JWTs signed with this key have INVALID signatures                  │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 4. Mathematical Background

### 4.1 EC P-256 Key Parameters

| Parameter | Description | Size | Base64URL Length |
|-----------|-------------|------|------------------|
| `d` | Private key (integer) | 32 bytes (256 bits) | 43 chars |
| `x` | Public key X coordinate | 32 bytes | 43 chars |
| `y` | Public key Y coordinate | 32 bytes | 43 chars |

### 4.2 Probability of Bug Occurrence

**Initial Estimate** (based on leading zero bytes):

| Leading Zero Bytes | Probability | Hex Length | Base64URL Length |
|-------------------|-------------|------------|------------------|
| 0 | 255/256 ≈ 99.61% | 64 chars | 43 chars ✓ |
| 1 | 1/256 ≈ 0.39% | 62 chars* | 42 chars ✗ |
| 2 | 1/65536 ≈ 0.0015% | 60 chars* | 41 chars ✗ |

*After incorrect zero-stripping

**Actual Observed Rate: ~5.6%** (see Section 8)

The observed rate is higher because `sed 's/^00*//'` strips **any** leading zeros in the hex string, not just complete `00` byte pairs. This includes cases where the hex starts with a single `0` character.

### 4.3 Base64URL Encoding Math

```
32 bytes = 256 bits
256 bits ÷ 6 bits/char = 42.67 → 43 base64url characters

31 bytes = 248 bits
248 bits ÷ 6 bits/char = 41.33 → 42 base64url characters
```

---

## 5. Affected Code Path

### 5.1 Call Chain

```
05_setup_notary-service.sh
    └── generate_asymmetric_signing_key()     # encryption_setup.sh:87
            └── D_VALUE extraction            # Line 112-115 (BUG)
            └── D_PADDED padding              # Line 146 (BUG)
            └── hex_to_base64url()            # Line 149
                    └── Returns corrupted 'd' value
```

### 5.2 Where the Key is Stored

```
OpenBao Path: services/nvcf-notary/kv/keys/signing-key
Fields:
  - keys: Base64-encoded JWKS containing the signing key
  - kid: Key ID (UUID)
  - alg: "ES256"
```

### 5.3 Where the Key is Used

The `nvcf-notary` service:
1. Retrieves the signing key from OpenBao
2. Uses it to sign JWTs for function invocation assertions
3. Downstream services verify these JWTs

---

## 6. Evidence

### 6.1 Corrupted Key from Cluster

```json
{
  "kty": "EC",
  "d": "qx2F_T5zOu2p6iMsJFGoHSX4VTILl8z_mQbyfuHDqA",  // 42 chars ✗
  "crv": "P-256",
  "kid": "b640a5a8-d7aa-11f0-871e-e637564d75f3",
  "x": "N5mw64h2O96Jw0z0XyCoj4X83zld_dj8dHD3EzEcyX8",  // 43 chars ✓
  "y": "Q8nC9HqwEjShMaG73lVNhhpgRJVSoEKws-L6TGxMBO8",  // 43 chars ✓
  "alg": "ES256"
}
```

### 6.2 Valid Key (After Fix)

```json
{
  "kty": "EC",
  "d": "5xMau19x1ujKI_xdDKKR6g7-MEn7c81Tv7Ea7FlVn9U",  // 43 chars ✓
  "crv": "P-256",
  "kid": "76a949fb-ce73-45dc-8f76-1c350ff86616",
  "x": "gIwtqavcjZ38ievWVpU1glsCqoqyn4AOjD_Rsua0SEE",  // 43 chars ✓
  "y": "jx5X9N2P8PtgKudPDaBu8zVHJmNGkP1crLcmUKwr1Nk",  // 43 chars ✓
  "alg": "ES256"
}
```

---

## 7. The Fix

### 7.1 Applied Fix (Line 146)

**Before:**
```bash
D_PADDED=$(printf "%064s" "$D_VALUE")
```

**After:**
```bash
# Note: printf %s pads with spaces, so we must replace them with zeros
D_PADDED=$(printf "%064s" "$D_VALUE" | tr ' ' '0')
```

### 7.2 Why This Works

The `tr ' ' '0'` converts all space characters to `0` characters, producing proper zero-padding:

```bash
# Before fix:
printf "%064s" "abc" → "                                                             abc"

# After fix:
printf "%064s" "abc" | tr ' ' '0' → "0000000000000000000000000000000000000000000000000000000000000abc"
```

---

## 8. Verification - Test Harness Results

A Go-based test harness was developed to statistically validate the bug and fix. The harness:
1. Calls the bash `generate_asymmetric_signing_key()` function
2. Parses the resulting JWK
3. Signs a JWT using Go's `crypto/ecdsa`
4. Verifies the signature
5. Tracks `d` parameter lengths and pass/fail rates

### 8.1 Environment Discovery

**Critical Finding**: The bug only manifests on **Linux**, not macOS.

```
=== Testing xxd behavior with spaces (the bug) ===
Padded: |                                                          abc123|
After xxd round-trip: |abc123|
Result length: 6 (should be 64 if working, less if broken)

=== Testing with zero padding (the fix) ===
Padded: |0000000000000000000000000000000000000000000000000000000000abc123|
After xxd round-trip: |0000000000000000000000000000000000000000000000000000000000abc123|
Result length: 64
```

Linux `xxd -r -p` silently drops space characters (invalid hex). macOS `xxd` handles this differently.

### 8.2 Test Results - Buggy Code (Linux Container)

```
╔══════════════════════════════════════════════════════════════╗
║          JWT Signing Key Test Harness (Go)                   ║
╠══════════════════════════════════════════════════════════════╣
║  Script:      encryption_setup_buggy.sh                       ║
║  Version:     BUGGY (Linux)                                   ║
║  Iterations:  1000                                            ║
╚══════════════════════════════════════════════════════════════╝

═════════════════════════════════════════════════════════════════
                         RESULTS
─────────────────────────────────────────────────────────────────
  Passed:         944  ( 94.40%)
  Failed:          56  (  5.60%)  ← Signature verification failed
  Parse Errors:     0  (  0.00%)

─────────────────────────────────────────────────────────────────
                   'd' LENGTH DISTRIBUTION
─────────────────────────────────────────────────────────────────
  40 chars:      1  (  0.10%)  ← CORRUPTED (2 leading zero bytes)
  41 chars:      0  (  0.00%)
  42 chars:     56  (  5.60%)  ← CORRUPTED (1 leading zero byte)
  43 chars:    943  ( 94.30%)  ← VALID

─────────────────────────────────────────────────────────────────
                       CONCLUSION
─────────────────────────────────────────────────────────────────
  Bug Confirmed:    YES
  Expected Rate:    ~0.39%
  Observed Rate:    5.60%
  Statistically Significant: ✓
═════════════════════════════════════════════════════════════════
```

### 8.3 Test Results - Fixed Code (Linux Container)

```
╔══════════════════════════════════════════════════════════════╗
║          JWT Signing Key Test Harness (Go)                   ║
╠══════════════════════════════════════════════════════════════╣
║  Script:      encryption_setup_fixed.sh                       ║
║  Version:     FIXED (Linux)                                   ║
║  Iterations:  1000                                            ║
╚══════════════════════════════════════════════════════════════╝

═════════════════════════════════════════════════════════════════
                         RESULTS
─────────────────────────────────────────────────────────────────
  Passed:        1000  (100.00%)
  Failed:           0  (  0.00%)  ← Signature verification failed
  Parse Errors:     0  (  0.00%)

─────────────────────────────────────────────────────────────────
                   'd' LENGTH DISTRIBUTION
─────────────────────────────────────────────────────────────────
  40 chars:      0  (  0.00%)
  41 chars:      0  (  0.00%)
  42 chars:      0  (  0.00%)
  43 chars:   1000  (100.00%)  ← VALID

─────────────────────────────────────────────────────────────────
                       CONCLUSION
─────────────────────────────────────────────────────────────────
  Bug Confirmed:    NO
  All keys valid:   ✓
═════════════════════════════════════════════════════════════════
```

### 8.4 Summary Comparison

| Version | Passed | Failed | d=43 (valid) | d<43 (corrupted) |
|---------|--------|--------|--------------|------------------|
| **BUGGY** | 944 (94.4%) | **56 (5.6%)** | 943 | 57 |
| **FIXED** | **1000 (100%)** | 0 (0%) | 1000 | 0 |

### 8.5 Running the Test Harness

```bash
# Build the harness for Linux
cd tests/signing-key-harness
GOOS=linux GOARCH=amd64 go build -o signing-key-harness-linux .

# Run in container (matches production environment)
docker run --rm \
  -v "$(pwd):/harness" \
  --entrypoint /bin/sh \
  openbao/openbao:2.2.2 \
  -c '
    apk add --no-cache openssl uuidgen bash > /dev/null 2>&1
    cd /harness
    ./signing-key-harness-linux --script ./testdata/encryption_setup_buggy.sh --version "BUGGY" --n 1000
  '
```

---

## 9. Remediation for Affected Clusters

Clusters that were deployed with the bug and have corrupted notary signing keys will need remediation:

### Option 1: Re-run Migration (Destructive)

```bash
# Delete and recreate the signing key
bao kv delete services/nvcf-notary/kv/keys/signing-key

# Re-run the notary migration
# (will generate a new valid key)
```

**Impact**: Existing signed JWTs become invalid immediately.

### Option 2: Manual Key Replacement

```bash
# Generate new valid key
NEW_KID=$(uuidgen | tr '[:upper:]' '[:lower:]')
# Use fixed generate_asymmetric_signing_key to create key
# Write to OpenBao with overwrite
```

### Option 3: Identify and Remediate

```bash
# Check if cluster is affected
bao kv get services/nvcf-notary/kv/keys/signing-key

# Decode the keys field and check 'd' length
# If 'd' is 42 chars, cluster is affected
```

---

## 10. Prevention

### 10.1 Code Review Checklist

- [ ] Cryptographic key generation preserves full key length
- [ ] Hex-to-base64url conversion handles all input lengths
- [ ] Unit tests verify key parameter lengths

### 10.2 Recommended Additional Fixes

Consider also reviewing line 115 (`sed 's/^00*//'`) to ensure leading zeros are handled correctly at the source, rather than relying solely on padding to fix truncated values.

---

## 11. Timeline

| Date | Event |
|------|-------|
| Unknown | Bug introduced in `generate_asymmetric_signing_key()` |
| Dec 2024 | JWT signature verification failure reported |
| Dec 2024 | Root cause identified: `d` value truncation due to space padding |
| Dec 2024 | Fix applied to `encryption_setup.sh` line 146-147 |
| Dec 2024 | Go test harness developed for statistical validation |
| Dec 2024 | Bug confirmed: 5.6% failure rate in Linux container |
| Dec 2024 | Fix validated: 100% pass rate in Linux container |
| Dec 2024 | Environment-specific behavior documented (Linux vs macOS) |

---

## 12. References

### Standards
- [RFC 7517 - JSON Web Key (JWK)](https://tools.ietf.org/html/rfc7517)
- [RFC 7518 - JSON Web Algorithms (JWA)](https://tools.ietf.org/html/rfc7518) - Section 6.2.2 (EC Private Key)
- [NIST FIPS 186-4](https://nvlpubs.nist.gov/nistpubs/FIPS/NIST.FIPS.186-4.pdf) - P-256 Curve specification

### Internal Documentation
- Test Harness: `tests/signing-key-harness/`
- Test Harness Design: `docs/signing-key-test-harness-design.md`
- Buggy Script (for testing): `tests/signing-key-harness/testdata/encryption_setup_buggy.sh`
- Fixed Script (for testing): `tests/signing-key-harness/testdata/encryption_setup_fixed.sh`

