# NVCF Notary

Notary is a microservice that issues short-lived signed JWTs ("assertions")
to authenticated callers. Services that need integrity-protected, verifiable
data carriers between two parties can deploy Notary alongside their stack
and have it sign arbitrary JSON payloads with an EC P-256 key whose public
half is published at `/.well-known/jwks.json`.

A typical use:

1. Caller obtains an OAuth2 access token from any OIDC-compliant token issuer.
2. Caller `POST`s a payload to Notary's `/sign` with that token in the
   `Authorization: Bearer ...` header.
3. Notary validates the caller token (issuer + scope + audience), wraps the
   payload as a JWT, signs it, and returns the assertion.
4. Downstream services verify the assertion against Notary's published JWKS.

## License

This project is licensed under the
[Apache License 2.0](../../../LICENSE). A complete attribution of third-party
libraries bundled in the Bazel-built application lives in [NOTICE](NOTICE).
Regenerate that file from the monorepo root:

```bash
bazel run //src/control-plane-services/notary:generate_notice -- \
  --update-metadata --write
```

Run the NOTICE drift test after any dependency change:

```bash
bazel test //src/control-plane-services/notary:notice_check_test
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contribution guide, including
the DCO sign-off requirement.

## Modules

| Module | Description |
|---|---|
| [notary-core](notary-core/) | Shared library: signing pipeline, validators, security config. Published to URM as `com.nvidia.notary:notary-core`. |
| [notary-service](notary-service/) | Spring Boot executable depending on `notary-core`. Built into the OSS Docker image. |

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Maven 3.8.7](https://maven.apache.org/download.cgi) or higher
- [Docker](https://docs.docker.com/get-docker/)

## Building

```bash
mvn clean package
```

Produces `notary-service/target/app.jar`.

## Running locally

Notary requires three runtime inputs:

1. An OAuth2 token issuer: Notary validates inbound caller tokens
   against this issuer's JWKS. Any OIDC-compliant provider works. Set:
   - `AUTH_TOKEN_ISSUER`: full issuer URI (e.g. `https://accounts.example.com`
     or `https://my-tenant.auth0.com/`). Spring fetches the issuer's
     `/.well-known/openid-configuration` (or `/.well-known/jwks.json`) to
     validate caller tokens.
   - `AUTH_TOKEN_SCOPE` (optional, defaults to `notary-sign`): required
     scope on caller tokens.
   - `NOTARY_REQUIRED_AUDIENCES_0`, `..._1`, ...: literal
     audiences a caller token must carry in its `aud` claim. Required
     by default (deployment fails at boot if unset); see
     `NOTARY_REQUIRE_AUDIENCE` below to opt into issuer-only
     validation instead.
   - `NOTARY_REQUIRE_AUDIENCE` (optional): set to `false` to
     disable the audience check. Validates caller JWTs against the
     issuer alone. Defaults to `true` so a misconfigured deployment
     fails fast rather than silently accepting any issuer-valid token.
2. `ASSERTION_ISSUER_URL`: value placed in the `iss` claim of the JWTs
   Notary signs.
3. A signing key rendered into a vault secrets file. Use the [test
   fixture](notary-core/src/test/resources/vault-agent/integration-test-vault.json)
   as a template, or generate a fresh keyset with `generate_jwks.sh` (see
   below).

```bash
export SPRING_PROFILES_ACTIVE=local
export AUTH_TOKEN_ISSUER=https://your-oauth2-provider.example.com
export ASSERTION_ISSUER_URL=http://assertion.issuer.test
export VAULT_SECRETS_JSON_PATH=file:/absolute/path/to/vault-secrets.json

java -jar notary-service/target/app.jar
```

Endpoints:

- `POST /sign` (port 8080): main signing endpoint, requires bearer token.
- `GET /.well-known/jwks.json` (port 8080): public-key set for assertion verification.
- `GET /actuator/health`, `/actuator/prometheus` (port 8181): operational endpoints.

## Configuration reference

| Environment variable | Description |
|---|---|
| `SPRING_PROFILES_ACTIVE` | One of `local`, `ncp`, or your own profile. Defaults to `local`. |
| `ASSERTION_ISSUER_URL` | Value placed in the `iss` claim of signed assertions. |
| `AUTH_TOKEN_ISSUER` | Full issuer URI Spring Security validates inbound caller JWTs against. |
| `AUTH_TOKEN_SCOPE` | Scope required on caller tokens. Defaults to `notary-sign`. |
| `NOTARY_REQUIRED_AUDIENCES_0`, `..._1`, ... | Indexed list of literal audiences required on caller JWTs. Required unless `NOTARY_REQUIRE_AUDIENCE=false`. |
| `NOTARY_REQUIRE_AUDIENCE` | `true` (default): fail at boot if no audiences set. `false`: validate against issuer alone. |
| `MAX_REQUEST_BODY_SIZE_BYTES` | Maximum payload size in bytes. Defaults to `4096`. JWTs need to fit in HTTP headers downstream, so keep this conservative. |
| `VAULT_SECRETS_JSON_PATH` | Path to the rendered vault secrets file containing the signing key set. Defaults to `vault/vault-secrets.json`; prefix with `file:` for absolute paths. |

### NCP profile

When using `ncp`:

- `AUTH_TOKEN_ISSUER`: issuer for inbound auth tokens.
- `AUTH_TOKEN_PUBLIC_KEYSET_URL`: URL of the public keyset for token validation.
- `AUTH_TOKEN_SCOPE`: required scope; defaults to `notary-sign`.

## Generating signing keys

The `generate_jwks.sh` helper script produces an EC P-256 keyset using only
OpenSSL and standard Unix utilities. No JVM is required. Keys carry timestamp
IDs in the format `kid-yyyyMMdd-HHmm`.

```bash
./generate_jwks.sh --keyset            # complete keyset (default)
./generate_jwks.sh --key               # single key
./generate_jwks.sh --escaped           # escaped keyset for vault put
```

Equivalent JVM-side test entrypoints exist in `notary-core` for environments
where the script is unavailable:

```bash
mvn -q clean test -Dtest=KeyGeneratorTest#generateInitialKeySet
mvn -q clean test -Dtest=KeyGeneratorTest#generateSigningKey
mvn -q clean test -Dtest=KeyGeneratorTest#generateInitialKeySetEscaped
```

## Monitoring

Notary exposes the standard Spring Boot Actuator surface on port 8181:

- `/actuator/health/{liveness,readiness}`: Kubernetes probes.
- `/actuator/prometheus`: metrics for scraping.
- `/actuator/metrics`: programmatic metric inspection.

The `/health` endpoint is also exposed on port 8080 without authentication,
for load-balancer health checks.

## Logging

Per-request log lines on `/sign` look like:

```
>>> signing using kid 'signing-kid' for client 'caller-client-id' and aud '[...]' with jti '...' at '...'
```

`com.nvidia.notary` is at `INFO` by default. Override per profile.
