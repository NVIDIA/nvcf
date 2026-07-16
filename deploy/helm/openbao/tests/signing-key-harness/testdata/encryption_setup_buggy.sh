#!/bin/bash
#
# BUGGY VERSION - For testing purposes only
# This version has the space-padding bug on line 147
#

# Include guard: Ensures this script's contents are only processed once.
if [[ -n "${_ENCRYPTION_SETUP_SH_SOURCED:-}" ]]; then
  return 0
fi
readonly _ENCRYPTION_SETUP_SH_SOURCED=1

# Function to check if OpenSSL is available
check_openssl() {
    if ! command -v openssl &> /dev/null; then
        echo "Error: OpenSSL is required but not installed."
        exit 1
    fi
}

# Function to generate data domain key
generate_rand_key() {
    local length=${1:-256}
    local type=${2:-hex}

    case $type in
        hex)
            local raw_key=$(openssl rand -hex $length)
            echo -n "$raw_key"
            ;;
        base64)
            # Don't use the -base64 flag; instead stream raw bytes directly to base64 to avoid null-byte truncation
            local base64_key=$(openssl rand $length | base64 -w 0 | tr -d '\n=' | tr '/+' '_-')
            echo -n "$base64_key"
            ;;
        *)
            echo "Error: Invalid type '$type'. Supported types: hex, base64" >&2
            return 1
            ;;
    esac
}

# Function to generate a random kid (Key ID)
generate_kid() {
    kid=$(uuidgen -t | tr '[:upper:]' '[:lower:]')
    echo "$kid"
}

# Main function to generate JWK
generate_jwk() {
    local kid=$1
    # Generate 256-bit (32 byte) random key
    local raw_key=$(openssl rand -base64 32)

    # Convert to base64url encoding (replace / with _, + with -, remove =)
    local base64url_key=$(echo -n "$raw_key" | tr '/+' '_-' | tr -d '=\n')

    # Create JWK JSON
    echo -n "{\"kty\":\"oct\",\"use\":\"enc\",\"kid\":\"$kid\",\"k\":\"$base64url_key\",\"alg\":\"A256GCM\"}"
}

generate_hmac_json() {
    local kid=$1
    local key_length=$2
    local key=$(generate_rand_key $key_length)
    echo -n "{\"keys\":[{\"kid\":\"$kid\",\"key\":\"$key\"}]}" | base64 -w 0
}

# Function to generate JWKS
generate_jwks_escaped_json() {
    local key=$1
    echo -n "{\"keys\":[$key]}"
}

# Function to generate JWKS
generate_base64_encoded_json() {
    local key=$1
    echo -n "{\"keys\":[$key]}" | base64 -w 0
}

base64_encode() {
    echo -n "$1" | base64 -w 0
}

# Base64URL encoding function (RFC 4648 Section 5)
base64url_encode() {
    base64 -w 0 | tr '+/' '-_' | tr -d '='
}

# Generate a new signing key
generate_asymmetric_signing_key() {

    if [ -z "$1" ]; then
        echo "Usage: generate_asymmetric_signing_key <kid>" >&2
        return 1
    fi

    # set KID
    KID=$1

    # Get current timestamp
    TIMESTAMP=$(date +%s)

    # Generate EC private key (P-256 curve)
    PRIVATE_KEY=$(openssl ecparam -genkey -name prime256v1 -noout)

    # Create temporary files for key processing
    TEMP_KEY=$(mktemp)
    TEMP_PUB=$(mktemp)
    trap "rm -f $TEMP_KEY $TEMP_PUB" EXIT

    # Save private key to temp file
    echo "$PRIVATE_KEY" > "$TEMP_KEY"

    # Extract the private key value (d parameter) - improved method
    D_VALUE=$(openssl ec -in "$TEMP_KEY" -text -noout 2>/dev/null | \
        awk '/priv:/{flag=1; next} /pub:/{flag=0} flag{print}' | \
        tr -d ' \n:' | \
        sed 's/^00*//')

    # Generate public key from private key
    openssl ec -in "$TEMP_KEY" -pubout -out "$TEMP_PUB" 2>/dev/null

    # Extract x and y coordinates from public key - improved method
    PUBLIC_HEX=$(openssl ec -pubin -in "$TEMP_PUB" -text -noout 2>/dev/null | \
        awk '/pub:/{flag=1; next} /ASN1 OID:/{flag=0} flag{print}' | \
        tr -d ' \n:')

    # Remove the 04 prefix (uncompressed point indicator) and extract x,y coordinates
    if [[ ${#PUBLIC_HEX} -ge 130 ]]; then
        COORDS=${PUBLIC_HEX:2}
        X_HEX=${COORDS:0:64}
        Y_HEX=${COORDS:64:64}
    else
        echo "Error: Could not extract public key coordinates" >&2
        exit 1
    fi

    # Convert hex to base64url (without padding)
    hex_to_base64url() {
        local hex_input="$1"
        # Ensure even length by padding with leading zero if needed
        if [[ $((${#hex_input} % 2)) -eq 1 ]]; then
            hex_input="0$hex_input"
        fi
        echo -n "$hex_input" | xxd -r -p | base64 | tr '+/' '-_' | tr -d '='
    }

    # Pad private key to 64 hex characters (32 bytes)
    # BUG: printf %s pads with SPACES, not zeros!
    D_PADDED=$(printf "%064s" "$D_VALUE")

    # Convert private key hex to base64url
    D_B64=$(hex_to_base64url "$D_PADDED")

    # Convert coordinates to base64url
    X_B64=$(hex_to_base64url "$X_HEX")
    Y_B64=$(hex_to_base64url "$Y_HEX")

    # Verify we got values
    if [[ -z "$D_B64" || -z "$X_B64" || -z "$Y_B64" ]]; then
        echo "Error: Failed to convert key values to base64url" >&2
        echo "D_VALUE: $D_VALUE" >&2
        echo "X_HEX: $X_HEX" >&2
        echo "Y_HEX: $Y_HEX" >&2
        exit 1
    fi

    # Output the JWK
    echo -n "{\"kty\":\"EC\",\"d\":\"$D_B64\",\"use\":\"sig\",\"crv\":\"P-256\",\"kid\":\"$KID\",\"x\":\"$X_B64\",\"y\":\"$Y_B64\",\"alg\":\"ES256\",\"iat\":$TIMESTAMP}"
}


# always check for openssl
check_openssl

