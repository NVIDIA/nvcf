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
	"context"
	"regexp"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
	"gopkg.in/square/go-jose.v2"
	"gopkg.in/square/go-jose.v2/jwt"
)

const (
	keyClaims  = "claims"
	keyHeaders = "headers"
)

func pathSign(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "sign/" + framework.GenericNameRegex(keyRoleName),
		Fields: map[string]*framework.FieldSchema{
			keyRoleName: {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role",
				Required:    true,
			},
			keyClaims: {
				Type:        framework.TypeMap,
				Description: `JSON claims set to sign.`,
				Required:    false,
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.UpdateOperation: &framework.PathOperation{
				Callback: b.pathSignWrite,
			},
			logical.ReadOperation: &framework.PathOperation{
				Callback: b.pathSignWrite,
			},
		},
		HelpSynopsis:    pathSignHelpSyn,
		HelpDescription: pathSignHelpDesc,
	}
}

func (b *backend) pathSignWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	roleName := d.Get("name").(string)

	role, err := b.getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return logical.ErrorResponse("unknown role"), logical.ErrInvalidRequest
	}

	// Gather "freeform" claims

	rawClaims, ok := d.GetOk(keyClaims)
	if !ok {
		rawClaims = map[string]interface{}{}
	}

	claims, ok := rawClaims.(map[string]any)
	if !ok {
		return logical.ErrorResponse("claims not a map"), logical.ErrInvalidRequest
	}

	config, err := b.getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}

	for claim := range claims {
		if allowedClaim, ok := config.allowedClaimsMap[claim]; !ok || !allowedClaim {
			return logical.ErrorResponse("claim %s not permitted", claim), logical.ErrInvalidRequest
		}
		if _, ok := role.Claims[claim]; ok {
			return logical.ErrorResponse("claim %s not permitted, already provided by role", claim), logical.ErrInvalidRequest
		}
	}

	for roleClaim := range role.Claims {
		claims[roleClaim] = role.Claims[roleClaim]
	}

	claims["iss"] = role.Issuer

	now := time.Now()

	expiry := now.Add(config.TokenTTL)
	claims["exp"] = jwt.NumericDate(expiry.Unix())

	if config.SetIAT {
		claims["iat"] = jwt.NumericDate(now.Unix())
	}

	if config.SetNBF {
		claims["nbf"] = jwt.NumericDate(now.Unix())
	}

	if config.SetJTI {
		jti, err := b.idGen.id()
		if err != nil {
			return logical.ErrorResponse("could not generate 'jti' claim: %v", err), err
		}
		claims["jti"] = jti
	}

	if rawSub, ok := claims["sub"]; ok {
		if sub, ok := rawSub.(string); ok {
			if matched, _ := regexp.MatchString(role.SubjectPattern, sub); !matched {
				return logical.ErrorResponse("validation of 'sub' claim failed (doesn't match role restriction)"), logical.ErrInvalidRequest
			}
			if matched, _ := regexp.MatchString(config.SubjectPattern, sub); !matched {
				return logical.ErrorResponse("validation of 'sub' claim failed (doesn't match config restriction)"), logical.ErrInvalidRequest
			}
		} else {
			return logical.ErrorResponse("'sub' claim was %T, not string"), logical.ErrInvalidRequest
		}
	}

	if rawAud, ok := claims["aud"]; ok {
		switch aud := rawAud.(type) {
		case string:
			if matched, _ := regexp.MatchString(role.AudiencePattern, aud); !matched {
				return logical.ErrorResponse("validation of 'aud' claim failed (doesn't match role restriction)"), logical.ErrInvalidRequest
			}
			if matched, _ := regexp.MatchString(config.AudiencePattern, aud); !matched {
				return logical.ErrorResponse("validation of 'aud' claim failed (doesn't match config restriction)"), logical.ErrInvalidRequest
			}
		case []interface{}:
			if config.MaxAudiences > -1 && len(aud) > config.MaxAudiences {
				return logical.ErrorResponse("too many audience claims: %d", len(aud)), logical.ErrInvalidRequest
			}
			for _, rawAudEntry := range aud {
				audEntry, ok := rawAudEntry.(string)
				if !ok {
					return logical.ErrorResponse("'aud' claim was %T, not string", audEntry), logical.ErrInvalidRequest
				}
				if matched, _ := regexp.MatchString(role.AudiencePattern, audEntry); !matched {
					return logical.ErrorResponse("validation of 'aud' claim failed (doesn't match role restriction)"), logical.ErrInvalidRequest
				}
				if matched, _ := regexp.MatchString(config.AudiencePattern, audEntry); !matched {
					return logical.ErrorResponse("validation of 'aud' claim failed (doesn't match config restriction)"), logical.ErrInvalidRequest
				}
			}
		default:
			return logical.ErrorResponse("'aud' claim was %T, not string or []string", rawAud), logical.ErrInvalidRequest
		}
	}

	policy, err := b.getPolicy(ctx, req.Storage, config, req.MountPoint)
	if err != nil {
		return logical.ErrorResponse("error getting key: %v", err), err
	}

	signer := &PolicySigner{
		BackendId:          b.id,
		SignatureAlgorithm: config.SignatureAlgorithm,
		Policy:             policy,
		SignerOptions:      (&jose.SignerOptions{}).WithType("JWT"),
	}

	for headerName := range role.Headers {
		headerValue := role.Headers[headerName]
		signer.SignerOptions = signer.SignerOptions.WithHeader(jose.HeaderKey(headerName), headerValue)
	}

	token, err := jwt.Signed(signer).Claims(claims).CompactSerialize()
	if err != nil {
		return logical.ErrorResponse("error serializing jwt: %v", err), err
	}

	resp := b.Secret(jwtSecretsTokenType).Response(
		map[string]interface{}{
			"token": token,
		},
		map[string]interface{}{},
	)
	resp.Secret.TTL = config.TokenTTL

	return resp, nil
}

const pathSignHelpSyn = `
Sign a set of claims.
`

const pathSignHelpDesc = `
Sign a set of claims.
`
