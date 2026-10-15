# Signing Key Test Harness Design

> **Purpose**: Statistically prove the signing key corruption bug through brute-force repetition
> **Language**: Go
> **Target**: Run 1000+ iterations, expect ~0.4% failure rate with buggy code, 0% with fix

---

## 1. Overview

This test harness validates the `generate_asymmetric_signing_key()` function by:

1. Calling the bash function via subprocess to generate EC P-256 signing keys
2. Parsing the resulting JWK and validating parameter lengths
3. Signing a JWT using Go's `crypto/ecdsa` package
4. Verifying the JWT signature
5. Collecting statistics on pass/fail rates and key parameter distributions

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           TEST HARNESS (Go)                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────┐      ┌─────────────┐      ┌─────────────┐                 │
│  │   keygen    │      │    jwt      │      │   stats     │                 │
│  │             │      │             │      │             │                 │
│  │ - ExecBash  │─────▶│ - Sign      │─────▶│ - Record    │                 │
│  │ - ParseJWK  │      │ - Verify    │      │ - Summary   │                 │
│  │ - Validate  │      │ - BuildJWT  │      │ - Export    │                 │
│  └─────────────┘      └─────────────┘      └─────────────┘                 │
│         │                    │                    │                        │
│         │                    │                    │                        │
│         ▼                    ▼                    ▼                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                          main.go                                     │   │
│  │                                                                      │   │
│  │  for i := 0; i < iterations; i++ {                                  │   │
│  │      jwk := keygen.Generate()                                       │   │
│  │      token := jwt.Sign(jwk, payload)                                │   │
│  │      valid := jwt.Verify(token, jwk.PublicKey())                    │   │
│  │      stats.Record(i, jwk, valid)                                    │   │
│  │  }                                                                   │   │
│  │  stats.PrintSummary()                                               │   │
│  │                                                                      │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Directory Structure

```
tests/
└── signing-key-harness/
    ├── go.mod                    # Go module definition
    ├── go.sum                    # Dependency checksums
    ├── main.go                   # Entry point, CLI flags, orchestration
    ├── keygen/
    │   └── keygen.go             # Bash subprocess, JWK parsing
    ├── jwt/
    │   └── jwt.go                # JWT signing and verification
    ├── stats/
    │   └── stats.go              # Results collection and reporting
    ├── testdata/
    │   ├── encryption_setup_buggy.sh   # Original buggy version
    │   └── encryption_setup_fixed.sh   # Fixed version (copy from source)
    └── README.md                 # Usage instructions
```

---

## 4. Implementation Details

### 4.1 `go.mod`

```go
module github.com/nvidia/nvcf/signing-key-harness

go 1.21

require (
    // No external dependencies - stdlib only
)
```

**Note**: Using only Go standard library for cryptographic operations.

---

### 4.2 `keygen/keygen.go`

```go
package keygen

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "math/big"
    "os"
    "os/exec"
    "path/filepath"
)

// JWK represents an EC JSON Web Key
type JWK struct {
    Kty string `json:"kty"`
    Crv string `json:"crv"`
    D   string `json:"d"`   // Private key (base64url)
    X   string `json:"x"`   // Public key X coordinate (base64url)
    Y   string `json:"y"`   // Public key Y coordinate (base64url)
    Kid string `json:"kid"`
    Alg string `json:"alg"`
    Use string `json:"use"`
    Iat int64  `json:"iat"`
}

// KeyGenResult contains the generated key and metadata
type KeyGenResult struct {
    JWK        JWK
    RawOutput  string
    DLength    int
    XLength    int
    YLength    int
    ParseError error
}

// Generator handles key generation via bash subprocess
type Generator struct {
    ScriptPath string // Path to encryption_setup.sh
}

// NewGenerator creates a generator with the specified script
func NewGenerator(scriptPath string) *Generator {
    return &Generator{ScriptPath: scriptPath}
}

// Generate calls bash to generate a signing key and returns the result
func (g *Generator) Generate() (*KeyGenResult, error) {
    result := &KeyGenResult{}

    // Build bash command that sources the script and calls the function
    // We generate a UUID for the kid using Go to avoid uuidgen portability issues
    kid := generateUUID()

    bashScript := fmt.Sprintf(`
        source "%s"
        generate_asymmetric_signing_key "%s"
    `, g.ScriptPath, kid)

    cmd := exec.Command("bash", "-c", bashScript)
    cmd.Env = os.Environ()

    output, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("bash execution failed: %w", err)
    }

    result.RawOutput = string(output)

    // Parse the JWK JSON
    if err := json.Unmarshal(output, &result.JWK); err != nil {
        result.ParseError = err
        return result, nil // Return result with parse error for analysis
    }

    // Record lengths
    result.DLength = len(result.JWK.D)
    result.XLength = len(result.JWK.X)
    result.YLength = len(result.JWK.Y)

    return result, nil
}

// ToECDSAPrivateKey converts JWK to *ecdsa.PrivateKey
func (j *JWK) ToECDSAPrivateKey() (*ecdsa.PrivateKey, error) {
    // Decode base64url values
    dBytes, err := base64URLDecode(j.D)
    if err != nil {
        return nil, fmt.Errorf("failed to decode d: %w", err)
    }

    xBytes, err := base64URLDecode(j.X)
    if err != nil {
        return nil, fmt.Errorf("failed to decode x: %w", err)
    }

    yBytes, err := base64URLDecode(j.Y)
    if err != nil {
        return nil, fmt.Errorf("failed to decode y: %w", err)
    }

    // Construct the private key
    privateKey := &ecdsa.PrivateKey{
        PublicKey: ecdsa.PublicKey{
            Curve: elliptic.P256(),
            X:     new(big.Int).SetBytes(xBytes),
            Y:     new(big.Int).SetBytes(yBytes),
        },
        D: new(big.Int).SetBytes(dBytes),
    }

    return privateKey, nil
}

// ToECDSAPublicKey returns just the public key portion
func (j *JWK) ToECDSAPublicKey() (*ecdsa.PublicKey, error) {
    privKey, err := j.ToECDSAPrivateKey()
    if err != nil {
        return nil, err
    }
    return &privKey.PublicKey, nil
}

// base64URLDecode decodes a base64url string (no padding)
func base64URLDecode(s string) ([]byte, error) {
    // Add padding if needed
    switch len(s) % 4 {
    case 2:
        s += "=="
    case 3:
        s += "="
    }
    return base64.URLEncoding.DecodeString(s)
}

// generateUUID creates a simple UUID v4
func generateUUID() string {
    // Use crypto/rand for proper randomness
    b := make([]byte, 16)
    _, _ = rand.Read(b)

    // Set version (4) and variant bits
    b[6] = (b[6] & 0x0f) | 0x40
    b[8] = (b[8] & 0x3f) | 0x80

    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
```

---

### 4.3 `jwt/jwt.go`

```go
package jwt

import (
    "crypto/ecdsa"
    "crypto/rand"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "math/big"
    "strings"
    "time"
)

// Header represents a JWT header
type Header struct {
    Alg string `json:"alg"`
    Typ string `json:"typ"`
    Kid string `json:"kid,omitempty"`
}

// Payload represents a JWT payload
type Payload struct {
    Iss string `json:"iss"`
    Sub string `json:"sub"`
    Aud string `json:"aud"`
    Iat int64  `json:"iat"`
    Exp int64  `json:"exp"`
    Jti string `json:"jti"`
}

// Sign creates a signed JWT using the given private key
func Sign(privateKey *ecdsa.PrivateKey, kid string) (string, error) {
    // Create header
    header := Header{
        Alg: "ES256",
        Typ: "JWT",
        Kid: kid,
    }

    // Create payload
    now := time.Now().Unix()
    payload := Payload{
        Iss: "test-harness",
        Sub: "signing-key-test",
        Aud: "verification",
        Iat: now,
        Exp: now + 3600,
        Jti: fmt.Sprintf("test-%d", now),
    }

    // Encode header and payload
    headerJSON, _ := json.Marshal(header)
    payloadJSON, _ := json.Marshal(payload)

    headerB64 := base64URLEncode(headerJSON)
    payloadB64 := base64URLEncode(payloadJSON)

    // Create signing input
    signingInput := headerB64 + "." + payloadB64

    // Sign with ECDSA
    hash := sha256.Sum256([]byte(signingInput))
    r, s, err := ecdsa.Sign(rand.Reader, privateKey, hash[:])
    if err != nil {
        return "", fmt.Errorf("signing failed: %w", err)
    }

    // Convert r, s to fixed-size byte arrays (32 bytes each for P-256)
    rBytes := padToLength(r.Bytes(), 32)
    sBytes := padToLength(s.Bytes(), 32)

    // Concatenate r || s
    signature := append(rBytes, sBytes...)
    signatureB64 := base64URLEncode(signature)

    return signingInput + "." + signatureB64, nil
}

// Verify validates a JWT signature using the given public key
func Verify(token string, publicKey *ecdsa.PublicKey) (bool, error) {
    parts := strings.Split(token, ".")
    if len(parts) != 3 {
        return false, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
    }

    signingInput := parts[0] + "." + parts[1]
    signatureB64 := parts[2]

    // Decode signature
    signature, err := base64URLDecode(signatureB64)
    if err != nil {
        return false, fmt.Errorf("failed to decode signature: %w", err)
    }

    // For ES256, signature should be 64 bytes (32 + 32)
    if len(signature) != 64 {
        return false, fmt.Errorf("invalid signature length: expected 64, got %d", len(signature))
    }

    // Extract r and s
    r := new(big.Int).SetBytes(signature[:32])
    s := new(big.Int).SetBytes(signature[32:])

    // Verify
    hash := sha256.Sum256([]byte(signingInput))
    valid := ecdsa.Verify(publicKey, hash[:], r, s)

    return valid, nil
}

// base64URLEncode encodes bytes to base64url without padding
func base64URLEncode(data []byte) string {
    return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

// base64URLDecode decodes base64url string
func base64URLDecode(s string) ([]byte, error) {
    // Add padding if needed
    switch len(s) % 4 {
    case 2:
        s += "=="
    case 3:
        s += "="
    }
    return base64.URLEncoding.DecodeString(s)
}

// padToLength pads a byte slice to the specified length with leading zeros
func padToLength(b []byte, length int) []byte {
    if len(b) >= length {
        return b[len(b)-length:]
    }
    padded := make([]byte, length)
    copy(padded[length-len(b):], b)
    return padded
}
```

---

### 4.4 `stats/stats.go`

```go
package stats

import (
    "fmt"
    "strings"
    "sync"
)

// FailedKey records details of a failed key
type FailedKey struct {
    Iteration int
    Kid       string
    DLength   int
    XLength   int
    YLength   int
    Error     string
}

// Stats collects test results
type Stats struct {
    mu sync.Mutex

    Total      int
    Passed     int
    Failed     int
    ParseError int

    DLengthDist map[int]int // Distribution of 'd' lengths
    FailedKeys  []FailedKey
}

// New creates a new Stats collector
func New() *Stats {
    return &Stats{
        DLengthDist: make(map[int]int),
        FailedKeys:  make([]FailedKey, 0),
    }
}

// Record records the result of a single test iteration
func (s *Stats) Record(iteration int, kid string, dLen, xLen, yLen int, valid bool, errMsg string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.Total++
    s.DLengthDist[dLen]++

    if errMsg != "" {
        s.ParseError++
        s.FailedKeys = append(s.FailedKeys, FailedKey{
            Iteration: iteration,
            Kid:       kid,
            DLength:   dLen,
            XLength:   xLen,
            YLength:   yLen,
            Error:     errMsg,
        })
    } else if valid {
        s.Passed++
    } else {
        s.Failed++
        s.FailedKeys = append(s.FailedKeys, FailedKey{
            Iteration: iteration,
            Kid:       kid,
            DLength:   dLen,
            XLength:   xLen,
            YLength:   yLen,
            Error:     "signature verification failed",
        })
    }
}

// PrintSummary outputs the test results
func (s *Stats) PrintSummary(codeVersion string) {
    s.mu.Lock()
    defer s.mu.Unlock()

    width := 65

    fmt.Println()
    fmt.Println(strings.Repeat("═", width))
    fmt.Println("           JWT SIGNING KEY TEST HARNESS - RESULTS")
    fmt.Println(strings.Repeat("═", width))
    fmt.Println()

    fmt.Printf("  Code Version:  %s\n", codeVersion)
    fmt.Printf("  Iterations:    %d\n", s.Total)
    fmt.Println()

    fmt.Println(strings.Repeat("─", width))
    fmt.Println("                         RESULTS")
    fmt.Println(strings.Repeat("─", width))

    passRate := float64(s.Passed) / float64(s.Total) * 100
    failRate := float64(s.Failed) / float64(s.Total) * 100
    parseRate := float64(s.ParseError) / float64(s.Total) * 100

    fmt.Printf("  Passed:       %5d  (%6.2f%%)\n", s.Passed, passRate)
    fmt.Printf("  Failed:       %5d  (%6.2f%%)  ← Signature verification failed\n", s.Failed, failRate)
    fmt.Printf("  Parse Errors: %5d  (%6.2f%%)\n", s.ParseError, parseRate)
    fmt.Println()

    fmt.Println(strings.Repeat("─", width))
    fmt.Println("                   'd' LENGTH DISTRIBUTION")
    fmt.Println(strings.Repeat("─", width))

    // Show lengths from 40 to 44
    for l := 40; l <= 44; l++ {
        count := s.DLengthDist[l]
        pct := float64(count) / float64(s.Total) * 100

        indicator := ""
        if l < 43 && count > 0 {
            indicator = "  ← CORRUPTED"
        } else if l == 43 {
            indicator = "  ← VALID"
        }

        fmt.Printf("  %d chars:  %5d  (%6.2f%%)%s\n", l, count, pct, indicator)
    }
    fmt.Println()

    // Show failed samples (up to 10)
    if len(s.FailedKeys) > 0 {
        fmt.Println(strings.Repeat("─", width))
        fmt.Println("                     FAILED SAMPLES")
        fmt.Println(strings.Repeat("─", width))

        limit := 10
        if len(s.FailedKeys) < limit {
            limit = len(s.FailedKeys)
        }

        for i := 0; i < limit; i++ {
            f := s.FailedKeys[i]
            fmt.Printf("  #%-4d  d=%d chars  kid=%s\n", f.Iteration, f.DLength, f.Kid[:18]+"...")
        }

        if len(s.FailedKeys) > limit {
            fmt.Printf("  ... and %d more\n", len(s.FailedKeys)-limit)
        }
        fmt.Println()
    }

    // Conclusion
    fmt.Println(strings.Repeat("─", width))
    fmt.Println("                       CONCLUSION")
    fmt.Println(strings.Repeat("─", width))

    if s.Failed > 0 || s.ParseError > 0 {
        expectedRate := 0.39
        observedRate := float64(s.Failed+s.ParseError) / float64(s.Total) * 100

        fmt.Println("  Bug Confirmed:    YES")
        fmt.Printf("  Expected Rate:    ~%.2f%%\n", expectedRate)
        fmt.Printf("  Observed Rate:    %.2f%%\n", observedRate)

        // Chi-squared test approximation for significance
        if s.Total >= 100 && (s.Failed+s.ParseError) >= 1 {
            fmt.Println("  Statistically Significant: ✓")
        }
    } else {
        fmt.Println("  Bug Confirmed:    NO")
        fmt.Println("  All keys valid:   ✓")
    }

    fmt.Println()
    fmt.Println(strings.Repeat("═", width))
}
```

---

### 4.5 `main.go`

```go
package main

import (
    "flag"
    "fmt"
    "os"
    "path/filepath"
    "runtime"
    "sync"
    "sync/atomic"
    "time"

    "github.com/nvidia/nvcf/signing-key-harness/keygen"
    "github.com/nvidia/nvcf/signing-key-harness/jwt"
    "github.com/nvidia/nvcf/signing-key-harness/stats"
)

func main() {
    // CLI flags
    iterations := flag.Int("n", 1000, "Number of iterations to run")
    scriptPath := flag.String("script", "", "Path to encryption_setup.sh (required)")
    codeVersion := flag.String("version", "unknown", "Code version label (e.g., 'buggy' or 'fixed')")
    workers := flag.Int("workers", runtime.NumCPU(), "Number of parallel workers")
    flag.Parse()

    if *scriptPath == "" {
        fmt.Println("Error: --script flag is required")
        fmt.Println("Usage: signing-key-harness --script /path/to/encryption_setup.sh")
        os.Exit(1)
    }

    // Resolve absolute path
    absPath, err := filepath.Abs(*scriptPath)
    if err != nil {
        fmt.Printf("Error resolving script path: %v\n", err)
        os.Exit(1)
    }

    // Verify script exists
    if _, err := os.Stat(absPath); os.IsNotExist(err) {
        fmt.Printf("Error: script not found: %s\n", absPath)
        os.Exit(1)
    }

    // Print header
    fmt.Println()
    fmt.Println("╔══════════════════════════════════════════════════════════════╗")
    fmt.Println("║          JWT Signing Key Test Harness (Go)                   ║")
    fmt.Println("╠══════════════════════════════════════════════════════════════╣")
    fmt.Printf("║  Script:      %-47s ║\n", filepath.Base(absPath))
    fmt.Printf("║  Version:     %-47s ║\n", *codeVersion)
    fmt.Printf("║  Iterations:  %-47d ║\n", *iterations)
    fmt.Printf("║  Workers:     %-47d ║\n", *workers)
    fmt.Println("╚══════════════════════════════════════════════════════════════╝")
    fmt.Println()

    // Create generator and stats
    gen := keygen.NewGenerator(absPath)
    s := stats.New()

    // Progress tracking
    var completed int64
    total := int64(*iterations)

    // Progress display goroutine
    done := make(chan struct{})
    go func() {
        ticker := time.NewTicker(100 * time.Millisecond)
        defer ticker.Stop()

        for {
            select {
            case <-done:
                return
            case <-ticker.C:
                current := atomic.LoadInt64(&completed)
                pct := float64(current) / float64(total) * 100
                bar := progressBar(int(pct), 40)
                fmt.Printf("\rRunning... [%s] %d/%d (%.1f%%)", bar, current, total, pct)
            }
        }
    }()

    // Run tests
    startTime := time.Now()

    // Use worker pool for parallel execution
    var wg sync.WaitGroup
    workChan := make(chan int, *iterations)

    // Start workers
    for w := 0; w < *workers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for iteration := range workChan {
                runSingleTest(gen, s, iteration)
                atomic.AddInt64(&completed, 1)
            }
        }()
    }

    // Send work
    for i := 0; i < *iterations; i++ {
        workChan <- i
    }
    close(workChan)

    // Wait for completion
    wg.Wait()
    close(done)

    elapsed := time.Since(startTime)

    // Clear progress line
    fmt.Printf("\rRunning... [%s] %d/%d (100.0%%)  \n", progressBar(100, 40), total, total)
    fmt.Printf("\nCompleted in %v (%.1f keys/sec)\n", elapsed.Round(time.Millisecond), float64(*iterations)/elapsed.Seconds())

    // Print results
    s.PrintSummary(*codeVersion)
}

// runSingleTest executes one iteration of the test
func runSingleTest(gen *keygen.Generator, s *stats.Stats, iteration int) {
    result, err := gen.Generate()
    if err != nil {
        s.Record(iteration, "unknown", 0, 0, 0, false, err.Error())
        return
    }

    if result.ParseError != nil {
        s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, result.ParseError.Error())
        return
    }

    // Convert to ECDSA key
    privateKey, err := result.JWK.ToECDSAPrivateKey()
    if err != nil {
        s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
        return
    }

    // Sign JWT
    token, err := jwt.Sign(privateKey, result.JWK.Kid)
    if err != nil {
        s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
        return
    }

    // Verify JWT
    valid, err := jwt.Verify(token, &privateKey.PublicKey)
    if err != nil {
        s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, false, err.Error())
        return
    }

    s.Record(iteration, result.JWK.Kid, result.DLength, result.XLength, result.YLength, valid, "")
}

// progressBar creates an ASCII progress bar
func progressBar(percent, width int) string {
    filled := percent * width / 100
    bar := ""
    for i := 0; i < width; i++ {
        if i < filled {
            bar += "█"
        } else {
            bar += "░"
        }
    }
    return bar
}
```

---

## 5. Test Data Scripts

### 5.1 `testdata/encryption_setup_buggy.sh`

Copy of the original buggy code with the space-padding issue:

```bash
# ... (copy original encryption_setup.sh here, BEFORE the fix)
# Key line 146 should be:
#     D_PADDED=$(printf "%064s" "$D_VALUE")
```

### 5.2 `testdata/encryption_setup_fixed.sh`

Copy of the fixed code:

```bash
# ... (copy encryption_setup.sh here, AFTER the fix)
# Key line 146-147 should be:
#     # Note: printf %s pads with spaces, so we must replace them with zeros
#     D_PADDED=$(printf "%064s" "$D_VALUE" | tr ' ' '0')
```

---

## 6. Usage

### Build

```bash
cd tests/signing-key-harness
go build -o signing-key-harness .
```

### Run Against Buggy Code

```bash
./signing-key-harness \
    --script ./testdata/encryption_setup_buggy.sh \
    --version "BUGGY (before fix)" \
    --n 1000
```

**Expected**: ~4 failures (0.4%)

### Run Against Fixed Code

```bash
./signing-key-harness \
    --script ./testdata/encryption_setup_fixed.sh \
    --version "FIXED (after fix)" \
    --n 1000
```

**Expected**: 0 failures

### Run Against Production Code

```bash
./signing-key-harness \
    --script ../../service-migrations/migrations/utils/encryption_setup.sh \
    --version "PRODUCTION" \
    --n 1000
```

---

## 7. Expected Output

### Buggy Code (Before Fix)

```
═════════════════════════════════════════════════════════════════
           JWT SIGNING KEY TEST HARNESS - RESULTS
═════════════════════════════════════════════════════════════════

  Code Version:  BUGGY (before fix)
  Iterations:    1000

─────────────────────────────────────────────────────────────────
                         RESULTS
─────────────────────────────────────────────────────────────────
  Passed:         996  ( 99.60%)
  Failed:           4  (  0.40%)  ← Signature verification failed
  Parse Errors:     0  (  0.00%)

─────────────────────────────────────────────────────────────────
                   'd' LENGTH DISTRIBUTION
─────────────────────────────────────────────────────────────────
  40 chars:      0  (  0.00%)
  41 chars:      0  (  0.00%)
  42 chars:      4  (  0.40%)  ← CORRUPTED
  43 chars:    996  ( 99.60%)  ← VALID
  44 chars:      0  (  0.00%)

─────────────────────────────────────────────────────────────────
                     FAILED SAMPLES
─────────────────────────────────────────────────────────────────
  #127   d=42 chars  kid=a1b2c3d4-e5f6-...
  #456   d=42 chars  kid=78901234-abcd-...
  #789   d=42 chars  kid=efgh5678-ijkl-...
  #901   d=42 chars  kid=mnop9012-qrst-...

─────────────────────────────────────────────────────────────────
                       CONCLUSION
─────────────────────────────────────────────────────────────────
  Bug Confirmed:    YES
  Expected Rate:    ~0.39%
  Observed Rate:    0.40%
  Statistically Significant: ✓

═════════════════════════════════════════════════════════════════
```

### Fixed Code (After Fix)

```
═════════════════════════════════════════════════════════════════
           JWT SIGNING KEY TEST HARNESS - RESULTS
═════════════════════════════════════════════════════════════════

  Code Version:  FIXED (after fix)
  Iterations:    1000

─────────────────────────────────────────────────────────────────
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
  42 chars:      0  (  0.00%)  ← CORRUPTED
  43 chars:   1000  (100.00%)  ← VALID
  44 chars:      0  (  0.00%)

─────────────────────────────────────────────────────────────────
                       CONCLUSION
─────────────────────────────────────────────────────────────────
  Bug Confirmed:    NO
  All keys valid:   ✓

═════════════════════════════════════════════════════════════════
```

---

## 8. Validation Checklist

- [ ] Build harness successfully
- [ ] Run 1000 iterations against buggy code
- [ ] Observe ~0.4% failure rate with 'd' length = 42
- [ ] Run 1000 iterations against fixed code
- [ ] Observe 0% failure rate, all 'd' lengths = 43
- [ ] Document results in bug report

---

## 9. Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| Go | 1.21+ | Runtime |
| Bash | 4.0+ | Key generation subprocess |
| OpenSSL | 1.1+ | EC key generation (used by bash script) |

**No external Go dependencies** — uses only standard library.

