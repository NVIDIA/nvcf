//
// Copyright 2021 Outfox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package jwtsecrets

import (
	"bytes"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/vault/sdk/helper/errutil"
	"github.com/hashicorp/vault/sdk/helper/keysutil"
	"gopkg.in/square/go-jose.v2"
	"strings"
)

type PolicySigner struct {
	BackendId          string
	SignatureAlgorithm jose.SignatureAlgorithm
	Policy             *keysutil.Policy
	SignerOptions      *jose.SignerOptions
}

func (ps *PolicySigner) Sign(payload []byte) (*jose.JSONWebSignature, error) {

	// Lock for entire sign operation to ensure no changes to versions happens
	ps.Policy.Lock(false)
	defer ps.Policy.Unlock()

	kid := createKeyId(ps.BackendId, ps.Policy.Name, ps.Policy.LatestVersion)

	protected := map[jose.HeaderKey]string{
		"kid": kid,
		"alg": string(ps.SignatureAlgorithm),
	}
	for k, v := range ps.SignerOptions.ExtraHeaders {
		protected[k] = fmt.Sprintf("%s", v)
	}

	serializedProtected, err := json.Marshal(protected)
	if err != nil {
		return nil, err
	}

	var input bytes.Buffer

	input.WriteString(base64.RawURLEncoding.EncodeToString(serializedProtected))
	input.WriteByte('.')
	input.WriteString(base64.RawURLEncoding.EncodeToString(payload))

	signature, err := ps.sign(input.Bytes())
	if err != nil {
		return nil, err
	}

	encodedSignature, err := json.Marshal(map[string]interface{}{
		"payload":   base64.RawURLEncoding.EncodeToString(payload),
		"protected": base64.RawURLEncoding.EncodeToString(serializedProtected),
		"signatures": []map[string]interface{}{
			{
				"protected": base64.RawURLEncoding.EncodeToString(serializedProtected),
				"signature": base64.RawURLEncoding.EncodeToString(signature),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return jose.ParseSigned(bytes.NewBuffer(encodedSignature).String())
}

func (ps *PolicySigner) sign(input []byte) ([]byte, error) {

	var hash crypto.Hash
	var hashType keysutil.HashType
	var sigAlg string
	switch ps.SignatureAlgorithm {
	case jose.RS256:
		hashType = keysutil.HashTypeSHA2256
		hash = crypto.SHA256
		sigAlg = "pkcs1v15"
	case jose.RS384:
		hashType = keysutil.HashTypeSHA2384
		hash = crypto.SHA384
		sigAlg = "pkcs1v15"
	case jose.RS512:
		hashType = keysutil.HashTypeSHA2512
		hash = crypto.SHA512
		sigAlg = "pkcs1v15"
	case jose.ES256:
		hashType = keysutil.HashTypeSHA2256
		hash = crypto.SHA256
		sigAlg = ""
	case jose.ES384:
		hashType = keysutil.HashTypeSHA2384
		hash = crypto.SHA384
		sigAlg = ""
	case jose.ES512:
		hashType = keysutil.HashTypeSHA2512
		hash = crypto.SHA512
		sigAlg = ""
	default:
		return nil, errutil.InternalError{Err: fmt.Sprintf("unsupported signature algorithm: %s", ps.SignatureAlgorithm)}
	}

	keyVersion := ps.Policy.LatestVersion

	hasher := hash.New()

	// According to documentation, Write() on hash never fails
	_, _ = hasher.Write(input)
	hashedInput := hasher.Sum(nil)

	result, err := ps.Policy.Sign(keyVersion, nil, hashedInput, hashType, sigAlg, keysutil.MarshalingTypeJWS)
	if err != nil {
		return nil, err
	}

	encodedSignature := strings.TrimPrefix(result.Signature, fmt.Sprintf("vault:v%d:", keyVersion))

	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return nil, err
	}

	return signature, nil
}

func (ps *PolicySigner) Options() jose.SignerOptions {
	return *ps.SignerOptions
}
