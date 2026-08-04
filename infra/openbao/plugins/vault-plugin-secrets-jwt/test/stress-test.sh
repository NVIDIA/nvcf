#!/bin/bash

# Configure vault
vault server -dev -dev-root-token-id="root" -config=/vault/config.hcl &
VAULT_PROC=$!

export VAULT_ADDR='http://127.0.0.1:8200'

pid=$$

fail() {
  pkill -P $pid
}

expect_equal() {
    # Usage: expect_equal op1 op2 message
    if [[ ! "$1" = "$2" ]]; then
        echo "$3: $1 != $2"
        fail
    fi
}

expect_match() {
    # Usage: expect_match str pattern message
    if [[ ! $1 =~ $2 ]]; then
        echo "$3: $1 does not match $2"
        fail
    fi
}

SHASUM=$(sha256sum "/vault/plugins/vault-plugin-secrets-jwt" | cut -d " " -f1)

vault login root

set -e

echo -e "\n### Register plugin"
vault plugin register -sha256 $SHASUM vault-plugin-secrets-jwt

echo -e "\n### Enable JWT engine at /jwt1 path"
vault secrets enable -path=jwt1 vault-plugin-secrets-jwt

echo -e "\n### Change the expiry time and make a pattern to check subjects against"
vault write jwt1/config "sig_alg=RS256" "key_ttl=3s" "jwt_ttl=40s"

echo -e "\n### Enable JWT engine at /jwt2 path"
vault secrets enable -path=jwt2 vault-plugin-secrets-jwt

echo -e "\n### Change the expiry time and make a pattern to check subjects against"
vault write jwt2/config "sig_alg=RS256" "key_ttl=3s" "jwt_ttl=40s"

stress() {

  echo -e "### [${1}] Adding role test${1}"
  if ! vault write jwt${2}/roles/test${1} issuer="DOOP"; then
    echo "Failed to add role"
    fail
  fi

  expected_sub=$(cat claims${3}.json | jq -r '.claims.sub')

  for i in {1..1000}; do
    echo -e "### [${1}] <${i}> Generating a token"
    if ! vault write -field=token jwt${2}/sign/test${1} @claims${3}.json > jwt-${1}-${i}.txt; then
      echo -e "##############################################"
      echo -e "### [${1}] <${i}> Failed to generate token ###"
      echo -e "##############################################"
      fail
    fi

    START_TIME="$(date -u +%s)"
    echo -e "### [${1}] <${i}> Validating 100 times"
    for j in {1..100}; do
#      echo -e "### [${1}] <${i}:${j}> Verify that the token is formatted as expected"
      if ! jwtverify "$(cat jwt-${1}-${i}.txt)" $VAULT_ADDR/v1/jwt${2}/jwks > decoded-${1}-${i}-${j}.txt; then
        echo -e "### [${1}] <${i}:${j}> Failed to verify token"
        fail
      fi

      expect_equal "$(cat decoded-${1}-${i}-${j}.txt | jq -r '.sub')" "${expected_sub}" "Wrong subject"
      expect_match "$(cat decoded-${1}-${i}-${j}.txt | jq '.exp')" "[0-9]+" "Invalid 'exp' claim"
      expect_match "$(cat decoded-${1}-${i}-${j}.txt | jq '.iat')" "[0-9]+" "Invalid 'iat' claim"
      expect_match "$(cat decoded-${1}-${i}-${j}.txt | jq '.nbf')" "[0-9]+" "Invalid 'nbf' claim"
    done
    END_TIME="$(date -u +%s)"

    ELAPSED_TIME="$(($END_TIME-$START_TIME))"
    if [[ $ELAPSED_TIME -gt 30 ]]; then
      echo -e "############################################################"
      echo -e "### [${1}] <${i}> Elapsed time: ${ELAPSED_TIME} seconds"
      echo -e "############################################################"
      fail
    fi

  done
}

for i in {1..10}; do
  stress $i "1" $i &
  sleep 1
done

for i in {1..10}; do
  stress $((i+10)) "2" $i &
  sleep 1
done
