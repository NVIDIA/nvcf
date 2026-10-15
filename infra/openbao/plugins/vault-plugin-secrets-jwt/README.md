# Vault Plugin: JWT Secrets
### A [Hashicorp Vault](https://www.github.com/hashicorp/vault) secrets plugin for generating and verifying JSON Web Tokens  

* [Overview](#overview)
* [Encryption And Key Managment](#encryption-and-key-management)
* [Usage](#usage)
  * [Quick Start](#quick-start)
  * [Container](#container)
  * [Configuration](#configuration)
  * [Roles](#roles)
  * [Signing](#signing)
* [Implementation Notes](#implementation-notes)
* [Contributors](#contributors)
* [Links](#quick-links)

# Overview

This plugin provides the ability to generate signed [JSON Web Tokens](https://jwt.io) (JWTs) without the signing keys
ever leaving Vault.

The plugin works by providing a service to sign JWTs using internal private key(s).
Simultaneously the plugin provides a [JSON Web Key](https://www.ietf.org/rfc/rfc7517.txt)
RFC compliant HTTP endpoint to publish public verification keys.

The plugin explicitly does not support verifying JWTs as a service; instead relying on clients to
fetch the verification keys via HTTP and verify JWTs locally. This dramatically reduces traffic to
Vault as well as allows clients to use standard client libraries for verification.

### ⚠️ Early Access 
The plugin is still under early development and should be tested thoroughly before being used in
any environment.

# Encryption and Key Management

## Automatic Key Rotation

The plugin automatically rotates signing keys and publishes a history of previous keys for
verification. The rotation schedule is configurable and ensures the keys will be available for
verification as long as any JWTs signed with them are valid.

## Supported Algorithms

The plugin supports a subset of the asymmetric encryption algorithms outlined in the JWT
specification.

* ES256
* ES384
* ES512
* RS256
* RS384
* RS512

Note: Due to its reliance on asymmetric encryption, the plugin will not support symmetric algorithms.

# Usage

## Quick Start
The plugin needs to be built and installed into your Vault instance's plugin directory prior
to any attempt at usage. A prepackaged container is available see [Container](#container).

1. Register the plugin


```bash
export PLUGIN_SHA=$(sha256sum $VAULT_PLUGIN_PATH/vault-plugin-secrets-jwt | cut -d ' ' -f1)
```

```bash
vault plugin register -sha256=$PLUGIN_SHA -command=vault-plugin-secrets-jwt secret jwt
```

2. Enable the plugin

```bash
vault secrets enable jwt
```

3. Create a role specifying the issuer (`iss`) claim of generated JWTs

```bash
vault write jwt/roles/test-role issuer=test.example.com
```
    
4. Sign a JWT (with default claims)
    
```bash
vault write -f jwt/sign/test-role
```

5. Retrieve JWKs for verification

```bash
curl https://$VAULT_ADDRESS/v1/jwt/jwks
```

## Container

A containerized version of Vault with the plugin pre-packaged inside is available for testing at
`https://hub.docker.com/r/outfoxx/vault`.

You can easily start a server in dev mode, that has the plugin enabled, using:
```bash
docker run --rm -P -e VAULT_DEV_ROOT_TOKEN_ID=root outfoxx/vault
```

## Configuration

The plugin has a usable (although probably not useful) default configuration. Although prior to usage
roles must be configured.

### 🔸 Allowed Claims

The plugin requires that any claims provided during role creation or JWT signing be explicitly
allowed in the configuration. By default, only the audience (`aud`) claim is allowed.

Allow `aud` and `groups` claims:

```bash
vault write jwt/config allowed_claims="aud" allowed_claims="groups" 
```

ℹ️ The `allowed_claims` field is a list, passing multiple values to `vault` cli allows you to
create a list.

### 🔸 Allowed Headers

The plugin requires that any headers provided during role creation be explicitly
allowed in the configuration.

Allow `iss` and `path` claims:

```bash
vault write jwt/config allowed_headers="iss" allowed_headers="path" 
```

ℹ️ The `allowed_headers` field is a list, passing multiple values to `vault` cli allows you to
create a list.

### 🔸 Signature Algorithm

The plugin allows configuration of the signature algorithm used to sign JWTs. By default, the
`ES256`algorithm is used.

```bash
vault write jwt/config sig_alg=RS256
```

When using an RSA algorithm (e.g. `RS256`) you can also select the size of the RSA key that
is generated. By default, a `2048` bit key is generated.

```bash
vault write jwt/config sig_alg=RS256 rsa_key_bits=4096
```

### 🔸 Key Rotation

Key rotation is automatically done by the plugin. You can configure the key rotation period to
whatever duration you wish.

```bash
vault write jwt/config key_ttl=12h0s
```

When keys are rotated the previous keys are kept to allow verification. Verification keys
are pruned at a time after which all generated tokens have expired.

### 🔸 Token TTL

Each generated JWT has a finite expiration. Configure the TTL used to determine each token's
expiration with the `token_ttl` field. By default, each token expires after `3m0s`.

```bash
vault write jwt/config token_ttl=3m
```

### 🔸 Audience & Subject Restrictions

The plugin can be configured to restrict the audience (`aud`) and subject (`sub`) claims to
those matching a specific pattern. By default, both claims are unrestricted.

```bash
vault write jwt/config subject_pattern=*.example.com
```

```bash
vault write jwt/config audience_pattern=*.example.com
```

Additionally, the audience (`aud`) claim (which is a list of stings) can be restricted to
a maximum length. By default, audience length is unrestricted.

```bash
vault write jwt/config max_audiences=2
```

### 🔸 Generated Reserved Claims

The issuer (`iss`) claim for generated tokens can be specified in the configuration. By
default, no issuer claim is added.

```bash
vault write jwt/config issuer=vault.example.com
```

The "unique token id" (`jti`) claim can be enabled/disabled. By default, a "unique token id" claim is added.

```bash
vault write jwt/config set_jti=true
```

The "not before" (`nbf`) claim can be enabled/disabled. By default, a "not before" claim is added.

```bash
vault write jwt/config set_nbf=true
```

The "issued at" (`iat`) claim can be enabled/disabled. By default, an "issued at" claim is added.

```bash
vault write jwt/config set_iat=true
```

## Roles

Before signing a JWT a role must be configured.

### 🔸 Issuer

When creating a role a value for the `issuer` field must be provided. The role issuer field specifies the
issuer (`iss`) claim for signed JWTs. This is the only method of providing the issuer claim for JWTs.

```bash
vault write jwt/roles/test-role issuer=test.example.com
```

### 🔸 Other Claims

Roles can additionally include any other claims that are allowed by the configuration.

```bash
echo claims '{"claims": {"groups":"test-group"}}' | vault write jwt/roles/test-role -
```

⚠️ Due to deficiencies of the `vault` cli, you need to pass `claims` in as JSON.

ℹ️ Any claims set in a role's `claims` field must be explicitly allowed in the
plugin's configuration and can no longer be set during a sign request.

### 🔸 Other Headers

Roles can additionally include any other headers that are allowed by the configuration.

```bash
echo claims '{"headers": {"iss":"some-key-issuer"}}' | vault write jwt/roles/test-role -
```

⚠️ Due to deficiencies of the `vault` cli, you need to pass `headers` in as JSON.

ℹ️ Any headers set in a role's `headers` field must be explicitly allowed in the
plugin's configuration.

### 🔸 Audience & Subject Restrictions

The role can be configured to restrict the audience (`aud`) and subject (`sub`) claims to
those matching a specific pattern; this restriction is in addition to the pattern restrictions
defined in the configuration. By default, both claims are unrestricted.

```bash
vault write jwt/roles/test-role subject_pattern=*.example.com
```

```bash
vault write jwt/roles/test-role audience_pattern=*.example.com
```

## Signing

Signing a JWT requires a role be configured and is easily done using the `sign` service,
providing the role name.

Sign a JWT with default configured claims.
```bash
vault write -f jwt/sign/test-role
```

Additionally, when signing a JWT, any claims allowed by the `allowed_claims` configuration and
can be specified.

```bash
echo claims '{"claims": {"groups":"test-group"}}' | vault write jwt/sign/test-role -
```

⚠️ If a claim value has been specified in the role's `claims` field, it cannot
be overridden during the sign request.

# Implementation Notes

## `keysutil` Usage 

The plugin uses the same mechanism as the builtin `Transit` secrets engine. Using `keysutil`
ensures the key management and rotation is built on a solid cryptographic engine.  

# Contributors

The original plugin started life as a learning exercise for [Ian Fox](https://github.com/ian-fox) and
I'd like to thank him for his initial proof-of-concept. As we make improvements he has kindly allowed
us to take over the project and move it forward.

We have taken the original proof-of-concept and rewrote it in hopes of providing a solid plugin that
can be used in production.

# Quick Links
    - Vault Website: https://www.vaultproject.io
    - Main Project Github: https://www.github.com/hashicorp/vault
    - JWT docs: https://jwt.io
