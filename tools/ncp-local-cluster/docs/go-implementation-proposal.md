# Go Credential Provider Implementation Proposal

This proposal outlines the steps and structure for converting the existing bash-based Kubelet credential provider to Go, adhering to the requirements in `docs/go-conversion-proposal.md`.

**1. Project Structure:**

We'll adopt a standard Go project structure:

```text
generic-credential-provider/
├── cmd/
│   └── generic-credential-provider/
│       └── main.go                 // Main application logic
├── internal/
│   ├── provider/
│   │   ├── provider.go             // Core logic for handling credentials
│   │   └── provider_test.go        // Unit tests for the provider logic
│   └── types/
│       └── types.go                // Struct definitions for JSON request/response
├── Makefile                        // Build and test automation
└── hack/                           // Scripts, e.g., for mock data generation (optional)
    └── mock_data/
        ├── input_request1.json
        └── docker_config1.json
```

**2. Core Logic (`internal/provider/provider.go` and `cmd/generic-credential-provider/main.go`):**

* **Input/Output Types (`internal/types/types.go`):**
  * Define Go structs for the Kubelet request JSON:

    ```go
    type KubeletRequest struct {
        Image         string `json:"image"`
        CacheKeyType  string `json:"cacheKeyType,omitempty"`
        CacheDuration string `json:"cacheDuration,omitempty"`
    }
    ```

  * Define Go structs for the Kubelet response JSON:

    ```go
    type AuthConfig struct {
        Username string `json:"username"`
        Password string `json:"password"`
    }

    type CredentialProviderResponse struct {
        Kind          string                `json:"kind"`
        APIVersion    string                `json:"apiVersion"`
        CacheKeyType  string                `json:"cacheKeyType"`
        CacheDuration string                `json:"cacheDuration"`
        Auth          map[string]AuthConfig `json:"auth"`
    }
    ```

* **Main Application Flow (`cmd/generic-credential-provider/main.go`):**
    1. **Argument Parsing:**
        * Use the `flag` package to parse command-line arguments.
        * Expect `get-credentials` as the first argument.
        * Expect `--config-file` with a path to the Docker `config.json`.
        * Implement validation for arguments as in the bash script.
    2. **Logging:**
        * Use the standard `log` package, potentially with a small wrapper to direct messages to `stderr` as `echo_err` does.
        * Include minimal logging for debug purposes and critical errors.
    3. **Input Reading:**
        * Read the JSON payload from `stdin` using `io.ReadAll(os.Stdin)`.
    4. **Call Core Logic:**
        * Instantiate and call the provider logic from `internal/provider`.

* **Provider Logic (`internal/provider/provider.go`):**
  * **Function Signature:** Design a primary function, e.g., `HandleGetCredentials(requestBody []byte, dockerConfigPath string) (*types.CredentialProviderResponse, error)`.
  * **JSON Deserialization (Request):**
    * Unmarshal the input JSON from `stdin` into the `KubeletRequest` struct using `encoding/json`.
    * Handle potential unmarshalling errors.
  * **Input Validation:**
    * Check if `Image` is present. If not, return an error or an empty JSON response (`{}`) as per Kubelet spec.
  * **Default Values:**
    * Set `CacheKeyType` to "Registry" if not provided or "null".
    * Set `CacheDuration` to "5m" if not provided or "null". (Note: The ADR mentions `defaultCacheDuration: "10m"` in `credential-provider-config.yaml`, which Kubelet might use if the provider omits these fields, but the bash script has its own defaults. We should clarify which default takes precedence or if the provider should always return these values based on its own logic if not in the request). For now, we'll stick to the bash script's defaults.
  * **Registry Extraction:**
    * Parse the `Image` string to extract the registry name. The logic in the bash script ( `awk -F'/' '{if (NF==1 || index($1, ".") == 0 && index($1, ":") == 0) {print "docker.io"} else {print $1}}'`) needs to be replicated in Go. This typically involves string splitting and checking for `.` or `:` in the first component.
  * **Docker Config Parsing:**
    * Read the `config.json` file specified by `--config-file`.
    * Use `encoding/json` to unmarshal it. A common structure for `config.json` is:

        ```go
        type DockerConfigFile struct {
            Auths map[string]DockerAuthEntry `json:"auths"`
            // Potentially other fields like HttpHeaders
        }
        type DockerAuthEntry struct {
            Auth string `json:"auth"`
            // Potentially other fields like Email
        }
        ```

    * Handle file reading and JSON parsing errors.
  * **Credential Lookup:**
    * Attempt to find credentials for the extracted registry in the parsed `config.json`.
    * First, try the plain registry name (e.g., `nvcr.io`).
    * If not found, try the `https://` prefixed registry name (e.g., `https://nvcr.io`).
    * If no credentials are found, return an empty JSON object (`{}`) and exit with status 0.
  * **Auth Token Decoding:**
    * The `auth` field from `config.json` is Base64 encoded (`username:password`).
    * Use `encoding/base64.StdEncoding.DecodeString()` to decode it.
    * Handle decoding errors (e.g., if the token is not valid Base64).
  * **Username/Password Extraction:**
    * Split the decoded string by the first colon (`:`) to get the username and password.
  * **Response Construction:**
    * Create an instance of `CredentialProviderResponse`.
    * Set `Kind` to `"CredentialProviderResponse"`.
    * Set `APIVersion` to `"credentialprovider.kubelet.k8s.io/v1"`.
    * Populate `CacheKeyType` and `CacheDuration` (using defaults if necessary).
    * Populate the `Auth` map with the registry as the key and an `AuthConfig` struct containing the username and password.
  * **JSON Serialization (Response):**
    * Marshal the `CredentialProviderResponse` struct to JSON using `encoding/json`.
    * Print the resulting JSON to `stdout`.
  * **Error Handling:**
    * Throughout the process, handle errors gracefully.
    * If an error occurs that prevents providing credentials, log the error to `stderr` and either exit with a non-zero status or output an empty JSON object (`{}`) to `stdout` as per the Kubelet credential provider specification (the bash script generally exits 1 on critical errors after printing `{}`). The ADR requires "adequate error handling". Returning an empty JSON and exiting 0 is often preferred by Kubelet for recoverable/non-fatal issues from its perspective.

**3. Testing (`internal/provider/provider_test.go`):**

* Use Go's built-in `testing` package.
* **Table-Driven Tests:** Implement table-driven tests for `HandleGetCredentials` covering various scenarios:
  * Valid input, credentials found (plain registry).
  * Valid input, credentials found (https:// registry).
  * Valid input, no credentials found for the registry.
  * Input missing `image`.
  * Input with `cacheKeyType` and `cacheDuration`.
  * Input without `cacheKeyType` and `cacheDuration` (test defaults).
  * Malformed input JSON.
  * Non-existent Docker config file.
  * Malformed Docker config file.
  * Docker config with invalid Base64 auth token.
  * Docker config with auth token but no colon.
* **Mock Data:**
  * Use string literals or embed small JSON files for mock Kubelet requests.
  * Create mock `config.json` files as temporary files during tests or use string literals.
* **Makefile Target:** Add a `test` target to the `Makefile`: `go test ./...`.

**4. Build Modifications (`Makefile`):**

* **Build Target:**

    ```makefile
    BINARY_NAME=generic-credential-provider
    CMD_PATH=./cmd/$(BINARY_NAME)
    OUTPUT_DIR?=./bin

    build:
        @echo "Building $(BINARY_NAME)..."
        GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) go build -o $(OUTPUT_DIR)/$(BINARY_NAME) $(CMD_PATH)/main.go

    # Allow overriding target arch
    build-linux-amd64:
        @echo "Building $(BINARY_NAME) for linux/amd64..."
        GOOS=linux GOARCH=amd64 go build -o $(OUTPUT_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)/main.go
    ```

* The default build will use the host's OS and architecture.
* Provide an example target (e.g., `build-linux-amd64`) for cross-compilation by setting `GOOS` and `GOARCH` environment variables.

**5. Deployment Modifications:**

* As stated in the ADR, these should be minimal. The primary change will be replacing the bash script with the compiled Go binary in the container image and ensuring it's mounted and executed with the same arguments:
    `args: ["get-credentials", "--config-file=/etc/kubernetes/secrets/docker-config.json"]`

**Addressing ADR Points:**

* **Existing logic remains intact:** The proposed Go logic mirrors the bash script's functionality step-by-step.
* **Idiomatic Go for JSON processing:** `encoding/json` with structs will be used.
* **Adequate error handling:** Each step includes error checks, and errors are logged. The behavior on error (exit code, output) will match the Kubelet spec.
* **Same input/argument signature:** The `flag` package will handle `--config-file`, and `stdin` will be used for the JSON request. The `get-credentials` command is the entry point.
* **Minimal logging to stdout/stderr:** The `log` package will be used, directing errors/debug info to `stderr` and the JSON response to `stdout`.
* **Tests:** A minimal set of positive and negative tests using mock data will be created with a `Makefile` target.
* **Build modifications:** The `Makefile` will be adjusted to build the Go binary, with considerations for target architecture.
* **Deployment modifications:** Confirmed to be minor, mainly replacing the executable.
