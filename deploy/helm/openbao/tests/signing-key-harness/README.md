# JWT Signing Key Test Harness

A Go-based test harness to validate the EC P-256 signing key generation function and statistically prove the bug/fix behavior.

## Overview

This harness:
1. Calls the bash `generate_asymmetric_signing_key()` function via subprocess
2. Parses the resulting JWK
3. Signs a JWT using Go's `crypto/ecdsa`
4. Verifies the signature
5. Tracks `d` parameter lengths and pass/fail rates

## Important: Linux-Only Bug

**The bug only manifests on Linux**, not macOS. This is because Linux `xxd -r -p` silently drops space characters (invalid hex), while macOS handles them differently.

**Always run tests inside a Docker container** to match the production environment.

---

## Quick Start

### 1. Build the Harness

```bash
cd tests/signing-key-harness

# Build for Linux (required for container testing)
GOOS=linux GOARCH=amd64 go build -o signing-key-harness-linux .

# Build for local macOS (for development only - won't reproduce bug)
go build -o signing-key-harness .
```

### 2. Run Tests

All tests should be run from the repository root directory.

---

## Test Variants

### Test Buggy Code (Before Fix)

Demonstrates the bug with ~5.6% failure rate:

```bash
cd /path/to/k8s-openbao

docker run --rm \
  -v "$(pwd)/tests/signing-key-harness:/harness" \
  --entrypoint /bin/sh \
  openbao/openbao:2.2.2 \
  -c '
    apk add --no-cache openssl uuidgen bash > /dev/null 2>&1
    cd /harness
    ./signing-key-harness-linux \
      --script ./testdata/encryption_setup_buggy.sh \
      --version "BUGGY (before fix)" \
      --n 1000 \
      --workers 1
  '
```

**Expected Result:**
- ~56 failures (5.6%)
- `d` lengths: mix of 40, 42, and 43 chars

---

### Test Fixed Code (After Fix)

Validates the fix with 100% pass rate:

```bash
cd /path/to/k8s-openbao

docker run --rm \
  -v "$(pwd)/tests/signing-key-harness:/harness" \
  --entrypoint /bin/sh \
  openbao/openbao:2.2.2 \
  -c '
    apk add --no-cache openssl uuidgen bash > /dev/null 2>&1
    cd /harness
    ./signing-key-harness-linux \
      --script ./testdata/encryption_setup_fixed.sh \
      --version "FIXED (after fix)" \
      --n 1000 \
      --workers 1
  '
```

**Expected Result:**
- 0 failures (0%)
- All `d` lengths: 43 chars

---

### Test Production Code

Tests the actual source file to verify the fix is deployed:

```bash
cd /path/to/k8s-openbao

docker run --rm \
  -v "$(pwd)/tests/signing-key-harness:/harness" \
  -v "$(pwd)/service-migrations/migrations/utils:/scripts:ro" \
  --entrypoint /bin/sh \
  openbao/openbao:2.2.2 \
  -c '
    apk add --no-cache openssl uuidgen bash > /dev/null 2>&1
    cd /harness
    ./signing-key-harness-linux \
      --script /scripts/encryption_setup.sh \
      --version "PRODUCTION" \
      --n 1000 \
      --workers 1
  '
```

**Expected Result (after fix is merged):**
- 0 failures (0%)
- All `d` lengths: 43 chars

---

## CLI Options

| Flag | Default | Description |
|------|---------|-------------|
| `--script` | (required) | Path to `encryption_setup.sh` |
| `--version` | `unknown` | Label for the code version being tested |
| `--n` | `1000` | Number of iterations to run |
| `--workers` | `NumCPU` | Number of parallel workers |

---

## Test Data Files

| File | Description |
|------|-------------|
| `testdata/encryption_setup_buggy.sh` | Original code with space-padding bug |
| `testdata/encryption_setup_fixed.sh` | Fixed code with `tr ' ' '0'` |

---

## Understanding the Output

```
═════════════════════════════════════════════════════════════════
                         RESULTS
─────────────────────────────────────────────────────────────────
  Passed:         944  ( 94.40%)    ← Keys that signed/verified OK
  Failed:          56  (  5.60%)    ← Signature verification failed
  Parse Errors:     0  (  0.00%)    ← JSON parse failures

─────────────────────────────────────────────────────────────────
                   'd' LENGTH DISTRIBUTION
─────────────────────────────────────────────────────────────────
  40 chars:      1  (  0.10%)  ← CORRUPTED (2+ leading zeros stripped)
  41 chars:      0  (  0.00%)
  42 chars:     56  (  5.60%)  ← CORRUPTED (1 leading zero stripped)
  43 chars:    943  ( 94.30%)  ← VALID (correct length)
  44 chars:      0  (  0.00%)
```

- **43 chars** = Valid key (32 bytes → 43 base64url chars)
- **<43 chars** = Corrupted key (leading zeros stripped, spaces ignored by `xxd`)

---

## Troubleshooting

### "Error: OpenSSL is required but not installed"

The container needs `openssl`, `uuidgen`, and `bash`:

```bash
apk add --no-cache openssl uuidgen bash
```

### Test completes instantly with 100% parse errors

This usually means the bash script failed to execute. Check:
1. Tools are installed in container
2. Script path is correct
3. Script has execute permissions

### macOS shows 0 failures for buggy code

This is expected! The bug only manifests on Linux due to different `xxd` behavior.
Always test in a Docker container.

---

## Related Documentation

- Bug Report: `docs/bug-report-notary-signing-key-corruption.md`
- Harness Design: `docs/signing-key-test-harness-design.md`

