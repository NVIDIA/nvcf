/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package oauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
)

const (
	DefaultJWKSKeySetTTL = 60 * time.Minute
)

// PublicKeyToPEM returns the pem encoded public key as string
func PublicKeyToPEM(pubKey crypto.PublicKey) (string, error) {
	keyBytes, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return "", err
	}
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: keyBytes,
	}
	pubKeyStr := string(pem.EncodeToMemory(block))
	return pubKeyStr, nil
}

// jwkKeyID computes a 10 byte (displayed as 20 char hex string) KeyID
// (kid) for JWK based on its JWK fields. This makes each JWK a
// content addressed object and we could use the kid to verify its
// integrity. Example kid: 7af11240784fdf1a0121
func jwkKeyID(jwk *jose.JSONWebKey) (string, error) {
	buf := new(bytes.Buffer)
	pem, err := PublicKeyToPEM(jwk.Key)
	if err != nil {
		return "", err
	}
	buf.WriteString(pem)
	buf.WriteString(jwk.Algorithm)
	buf.WriteString(jwk.Use)

	fullHash := sha256.New().Sum(buf.Bytes())
	hash := fullHash[0:10]

	return hex.EncodeToString(hash), nil
}

// NewJWKFromRSAPub creates jose.JSONWebKey object from a
// rsa.PublicKey, with its KeyID set to the hash computed by jwkKeyID.
func NewJWKFromRSAPub(pub *rsa.PublicKey) (*jose.JSONWebKey, error) {
	jwk := &jose.JSONWebKey{
		Algorithm: "RS256",
		Use:       "sig",
		Key:       pub,
	}

	keyID, err := jwkKeyID(jwk)
	if err != nil {
		return nil, fmt.Errorf("failed to compute jwkKeyID")
	}
	jwk.KeyID = keyID
	return jwk, nil
}

// NewJWKSFromRSAPubKeys returns jose.JSONWebKeySet from a list of RSA
// public keys.
func NewJWKSFromRSAPubKeys(pubKeys []*rsa.PublicKey) (*jose.JSONWebKeySet, error) {
	keyset := &jose.JSONWebKeySet{}
	for _, pub := range pubKeys {
		jwk, err := NewJWKFromRSAPub(pub)
		if err != nil {
			return nil, err
		}
		keyset.Keys = append(keyset.Keys, *jwk)
	}
	return keyset, nil
}

// NewJWKSHandler returns a http handler the implements
// .well-known/jwks.json API, given a list of known public keys.
func NewJWKSHandler(pubKeys []*rsa.PublicKey) http.HandlerFunc {
	handler := func(w http.ResponseWriter, r *http.Request) {
		log := core.GetLogger(r.Context())

		keyset, err := NewJWKSFromRSAPubKeys(pubKeys)
		if err != nil {
			log.Errorf("NewJWKSFromRSAPubKeys failed, err: %v", err)
			http.Error(w, "NewJWKSFromRSAPubKeys failed", http.StatusInternalServerError)
			return
		}

		resp, err := json.Marshal(keyset)
		log.Infof("jwks.json response %q, err: %v", string(resp), err)
		if err != nil {
			log.Errorf("Failed to json.Marshal keyset %+v, err: %v", keyset, err)
			http.Error(w, "Failed to json.Marshal keyset", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, errWrite := w.Write(resp)
		if errWrite != nil {
			log.Error(errWrite)
			http.Error(w, errWrite.Error(), http.StatusInternalServerError)
			return
		}
	}

	return http.HandlerFunc(handler)
}

// JWKSCache is a helper client cache struct that fetch public keys
// from EPS .well-known/jwks.json endpoint. Example usage in EMS:
//
// jc := NewJWKSCache("https://eps.egx.nvidia.com/.well-known/jwks.json")
//
// err := jc.Refresh()
// if err != nil { ... }
//
// epsPub, err := jc.Get(deviceEnrollmentRequest.KeyVersion)
// if err != nil { ... }
//
// err := VerifyEnrollmentRequest(deviceEnrollmentRequest, epsPub)
type JWKSCache struct {
	// URL is the .well-known/jwks.json url, e.g.,
	// https://eps.egx.nvidia.com/.well-known/jwks.json
	URL string

	// dependencies
	RoundTrip func(*http.Request) (*http.Response, error)

	// internal states
	mtx     sync.Mutex
	keyByID map[string]jose.JSONWebKey
}

func NewJWKSCache(url string) *JWKSCache {
	httpClient := http.Client{
		Timeout: 10 * time.Second,
	}
	return &JWKSCache{
		URL:       url,
		RoundTrip: httpClient.Do,
	}
}

func (jc *JWKSCache) Refresh() error {
	req, err := http.NewRequest("GET", jc.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := jc.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	keyset := jose.JSONWebKeySet{}
	if err := json.Unmarshal(bodyBytes, &keyset); err != nil {
		return err
	}

	keyByID := map[string]jose.JSONWebKey{}
	for _, jwk := range keyset.Keys {
		keyByID[jwk.KeyID] = jwk
	}

	jc.mtx.Lock()
	defer jc.mtx.Unlock()
	jc.keyByID = keyByID

	return nil
}

func (jc *JWKSCache) GetJSONWebKeySet() jose.JSONWebKeySet {
	keyset := jose.JSONWebKeySet{}
	for _, v := range jc.keyByID {
		keyset.Keys = append(keyset.Keys, v)
	}
	return keyset
}

// verifyJWKByKeyID is the dual method of jwkKeyID, it verifies a jwk
// using its content based KeyID.
func verifyJWKByKeyID(jwk *jose.JSONWebKey) error {
	expectedKeyID, err := jwkKeyID(jwk)
	if err != nil {
		return err
	}
	if jwk.KeyID != expectedKeyID {
		return fmt.Errorf("jwk.KeyID does not match its content addressed hash")
	}
	return nil
}

func (jc *JWKSCache) Get(kid string) (*rsa.PublicKey, error) {
	jc.mtx.Lock()
	defer jc.mtx.Unlock()
	if jc.keyByID == nil {
		return nil, fmt.Errorf("jc.keyset is nil, call Refresh() to update the cache before GetPublicKey()")
	}

	jwk, ok := jc.keyByID[kid]
	if !ok {
		return nil, fmt.Errorf("key with id %s not found", kid)
	}

	err := verifyJWKByKeyID(&jwk)
	if err != nil {
		return nil, fmt.Errorf("jwk is not valid %v", err)
	}

	pub, ok := jwk.Key.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("jwk is not a rsa key")
	}

	return pub, nil
}

func (jc *JWKSCache) GetOrRefresh(kid string) (*rsa.PublicKey, error) {
	pub, err := jc.Get(kid)
	if err == nil {
		return pub, nil
	}

	// Refresh cache and try one more time
	if err := jc.Refresh(); err != nil {
		return nil, err
	}
	return jc.Get(kid)
}

func GetJWKSKeysURL(etsEndpoint string) string {
	return fmt.Sprintf("%s/v1/identity/oidc/.well-known/keys", etsEndpoint)
}

// JWKSVerifier acts as a token verifier for
// to be consumed by the
type JWKSVerifier struct {
	sync.Mutex
	jwksCache   *JWKSCache
	cacheTTL    time.Duration
	cacheExpiry time.Time
	nowFunc     func() time.Time
}

type JWKSVerifierOption func(*JWKSVerifier)

func WithJWKSVerifierCacheTTL(cacheTTL time.Duration) JWKSVerifierOption {
	return func(j *JWKSVerifier) {
		j.cacheTTL = cacheTTL
	}
}

func NewJWKSVerifier(jwksURL string, options ...JWKSVerifierOption) *JWKSVerifier {
	verifier := &JWKSVerifier{
		jwksCache: NewJWKSCache(jwksURL),
		cacheTTL:  DefaultJWKSKeySetTTL,
		//nolint gocritic
		nowFunc: func() time.Time {
			return time.Now()
		},
	}
	for _, opt := range options {
		opt(verifier)
	}
	return verifier
}

func (v *JWKSVerifier) JWKSURL() string {
	return v.jwksCache.URL
}

func (v *JWKSVerifier) ExtractVerifiedToken(ctx context.Context, token string) (JWT, error) {
	log := core.GetLogger(ctx)

	jwks, err := v.getJSONWebKeySet(ctx)
	if err != nil {
		log.Errorf("failed to retrieve JSON web key set from cache, error: %v", err)
		return JWT{}, err
	}

	// Parse the token as a signed token
	tok, err := josejwt.ParseSigned(token, []jose.SignatureAlgorithm{
		jose.EdDSA,
		jose.HS256,
		jose.HS384,
		jose.HS512,
		jose.RS256,
		jose.RS384,
		jose.RS512,
		jose.ES256,
		jose.ES384,
		jose.PS256,
		jose.PS384,
		jose.PS512,
	})
	if err != nil {
		log.Debugf("parsing the signed token failed, error: %v", err)
		return JWT{}, err
	}

	jwtToken := JWT{}
	err = tok.Claims(jwks, &jwtToken)
	if err != nil {
		log.Debugf("verifying the claims failed, error: %v", err)
		return JWT{}, err
	}

	log.Debug("token verified successfully")
	return jwtToken, nil
}

// VerifyToken verifies that the current token is valid.
// Returns true if valid, false otherwise. An error is returned
// only when the validity of the token could not be determined.
func (v *JWKSVerifier) VerifyToken(ctx context.Context, token string) (bool, error) {
	log := core.GetLogger(ctx)

	jwks, err := v.getJSONWebKeySet(ctx)
	if err != nil {
		log.Errorf("failed to retrieve JSON web key set from cache, error: %v", err)
		return false, err
	}

	// Parse the token as a signed token
	tok, err := josejwt.ParseSigned(token, []jose.SignatureAlgorithm{
		jose.EdDSA,
		jose.HS256,
		jose.HS384,
		jose.HS512,
		jose.RS256,
		jose.RS384,
		jose.RS512,
		jose.ES256,
		jose.ES384,
		jose.PS256,
		jose.PS384,
		jose.PS512,
	})
	if err != nil {
		log.Errorf("parsing the signed token failed, error: %v", err)
		return false, err
	}

	// verify the JWKS public keys were used to sign this token
	err = tok.Claims(jwks, &map[string]interface{}{})
	if err != nil {
		log.Errorf("verifying the claims failed, error: %v", err)
		// When a JWKS does not contain a JWK with a
		// key ID which matches one in the provided tokens headers
		// this handles the case where the JWKS changed
		if err == jose.ErrJWKSKidNotFound {
			return false, nil
		}
		return false, err
	}

	log.Debug("token verified successfully")
	return true, nil
}

func (v *JWKSVerifier) getJSONWebKeySet(ctx context.Context) (jose.JSONWebKeySet, error) {
	v.Lock()
	defer v.Unlock()
	log := core.GetLogger(ctx)

	// Check if the current time has expired, if not
	// don't bother refresh the cache
	now := v.nowFunc()
	if now.After(v.cacheExpiry) {
		log.Debugf("token verifier has hit expiry time %s refresh the cache", v.cacheExpiry)
		err := v.jwksCache.Refresh()
		if err != nil {
			log.Errorf("error refreshing the JWKS cache, error: %v", err)
			return jose.JSONWebKeySet{}, err
		}
		// Set new expiration time upon success only
		v.cacheExpiry = now.Add(v.cacheTTL)
		log.Debugf("JWKS cache refreshed successfully setting new expiry time to %s", v.cacheExpiry)
	}

	return v.jwksCache.GetJSONWebKeySet(), nil
}
