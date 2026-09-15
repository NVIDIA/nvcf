#!/bin/bash

# Configure vault
vault server -dev -dev-root-token-id="root" -config=/vault/config.hcl &
VAULT_PROC=$!

export VAULT_ADDR='http://127.0.0.1:8200'

expect_equal() {
    # Usage: expect_equal op1 op2 message
    if [[ ! "$1" = "$2" ]]; then
        echo "$3: $1 != $2"
        exit 1
    fi
}

expect_not_equal() {
    # Usage: expect_equal op1 op2 message
    if [[ $1 = $2 ]]; then
        echo "$3: $1 = $2"
        exit 1
    fi
}

expect_match() {
    # Usage: expect_match str pattern message
    if [[ ! $1 =~ $2 ]]; then
        echo "$3: $1 does not match $2"
        exit 1
    fi
}

expect_no_match() {
    # Usage: expect_no_match str pattern message
    if [[ $1 =~ $2 ]]; then
        echo "$3: $1 matches $2"
        exit 1
    fi
}

SHASUM=$(sha256sum "/vault/plugins/vault-plugin-secrets-jwt" | cut -d " " -f1)

vault login root

set -e

echo -e "\n\n### Register plugin"
vault plugin register -sha256 $SHASUM vault-plugin-secrets-jwt

echo -e "\n\n### Enable JWT engine at /jwt path"
vault secrets enable -path=jwt vault-plugin-secrets-jwt

echo -e "\n\n### Change the expiry time and make a pattern to check subjects against"
vault write jwt/config "key_ttl=3s" "jwt_ttl=3s" "subject_pattern=^[A-Z][a-z]+ [A-Z][a-z]+$"

echo -e "\n\n### Attempt to create a token before role is created"
if vault write -field=token jwt/sign/test @claims.json; then
    echo "Signing with unknown role incorrectly succeeded."
    exit 1
fi

echo -e "\n\n### Attempt to create a role with a disallowed claim value"
if  vault write jwt/roles/test @claims_foo.json; then
    echo "Creating a role with a disallowed claim value incorrectly succeeded."
    exit 1
fi

echo -e "\n\n### Adding role test"
vault write jwt/roles/test issuer="DOOP"

echo -e "\n\n### Reading role test"
vault read jwt/roles/test

echo -e "\n\n### Create a token with test role"
vault write -field=token jwt/sign/test @claims.json > jwt1.txt

echo -e "\n\n### Verify that the token is formatted as expected"
jwtverify "$(cat jwt1.txt)" $VAULT_ADDR/v1/jwt/jwks | tee decoded.txt
expect_equal "$(cat decoded.txt | jq '.sub')" '"Zapp Brannigan"' "Wrong subject"
expect_match "$(cat decoded.txt | jq '.exp')" "[0-9]+" "Invalid 'exp' claim"
expect_match "$(cat decoded.txt | jq '.iat')" "[0-9]+" "Invalid 'iat' claim"
expect_match "$(cat decoded.txt | jq '.nbf')" "[0-9]+" "Invalid 'nbf' claim"

EXP_TIME=$(cat decoded.txt | jq '.exp')
IAT_TIME=$(cat decoded.txt | jq '.iat')
if [[ "(( EXP_TIME - IAT_TIME ))" -ne 3 ]]; then
    echo "times don't match"
    exit 1
fi

echo -e "\n\n### Switch to RSA 256 algorithm"
vault write jwt/config "sig_alg=RS256"

echo -e "\n\n### Wait and generate a second jwt"
sleep 3
vault write jwt/config "set_iat=false"
vault write -field=token jwt/sign/test @claims.json > jwt2.txt

echo -e "\n\n### Verify that the second token is formatted as expected"
jwtverify "$(cat jwt2.txt)" $VAULT_ADDR/v1/jwt/jwks | tee decoded2.txt

echo -e "\n\n### Verify that key rotation happened"
expect_not_equal "$(wget -qO- $VAULT_ADDR/v1/jwt/jwks | jq '.keys | length')" "1" "Key Not Rotated"

echo -e "\n\n### Verify second token does not have an iat claim"
expect_no_match "$(cat decoded2.txt)" "iat" "should not have 'iat' claim"

echo -e "\n\n### Verify that tokens have different unique ids"
expect_not_equal "$(cat decoded.txt | jq '.jti')" "$(cat decoded2.txt | jq '.jti')" "JTI claims should differ"

echo -e "\n\n### Attempt to sign with a claim that has an invalid value"
if vault write -field=token jwt/sign/test @invalid_claims.json; then
    echo "Writing an invalid subject claim incorrectly succeeded."
    exit 1
fi

echo -e "\n\n### Attempt to sign with a claim that has a disallowed claim"
if vault write -field=token jwt/sign/test @claims_foo.json; then
    echo "Writing a set of claims which contains a disallowed claim."
    exit 1
fi

echo -e "\n\n### Allow 'foo' claim"
vault write -field=allowed_claims jwt/config @allowed_claims.json

echo -e "\n\n### Verify signing now allows 'foo' claim"
vault write -field=token jwt/sign/test @claims_foo.json > jwt3.txt

echo -e "\n\n### Verify third token is formatted as expected"
jwtverify "$(cat jwt3.txt)" $VAULT_ADDR/v1/jwt/jwks | tee decoded3.txt
expect_equal "$(cat decoded3.txt | jq '.foo')" '"bar"' "jwt should have 'foo' field set"
