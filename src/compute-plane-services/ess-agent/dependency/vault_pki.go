/*
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0

Not a contribution
Changes made by NVIDIA CORPORATION & AFFILIATES enabling PKI renewal tuning or otherwise documented as
NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
<NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
*/
package dependency

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// added for nv
// pkiLeaseJitterPercentage MUST be a positive value
const pkiLeaseJitterPercentage = 2

// Ensure implements
var _ Dependency = (*VaultPKIQuery)(nil)

// added for nv
// ensure VaultPKIQuery implements NvDependency interface
var _ NvDependency = (*VaultPKIQuery)(nil)

// Return type containing PEMs as strings
type PemEncoded struct{ Cert, Key, CA string }

// a wrapper to mimic v2 secrets Data wrapper
func (p PemEncoded) Data() PemEncoded {
	return p
}

// VaultPKIQuery is the dependency to Vault for a secret
type VaultPKIQuery struct {
	stopCh  chan struct{}
	sleepCh chan time.Duration

	pkiPath  string
	data     map[string]interface{}
	filePath string

	// added for nv
	nv NvVaultQuery
}

// NvVaultPKIQueryInput
// added for nv
type NvVaultPKIQueryInput struct {
	UrlPath                     string
	FilePath                    string
	Data                        map[string]any
	ErrorOnMissingKey           bool
	RunOnce                     bool
	SkipMountVersionCheck       bool
	ExitOnClientError           bool
	StopProcessingOnClientError bool
	Destination                 string
	TemplateID                  string
}

// NewNvVaultPKIQuery creates a new datacenter dependency.
// added for nv
func NewNvVaultPKIQuery(i *NvVaultPKIQueryInput) (*VaultPKIQuery, error) {
	query, err := NewVaultPKIQuery(i.UrlPath, i.FilePath, i.Data)
	if err != nil {
		return nil, err
	}
	query.nv.errorOnMissingKeyEnabled = i.ErrorOnMissingKey
	query.nv.runOnce = i.RunOnce
	query.nv.skipMountVersionCheck = i.SkipMountVersionCheck
	query.nv.exitOnClientError = i.ExitOnClientError
	query.nv.stopProcessingOnClientError = i.StopProcessingOnClientError
	query.nv.destination = i.Destination
	query.nv.templateID = i.TemplateID
	return query, nil
}

// NewVaultPKIQuery creates a new datacenter dependency.
func NewVaultPKIQuery(urlpath, filepath string, data map[string]interface{}) (*VaultPKIQuery, error) {
	urlpath = strings.TrimSpace(urlpath)
	urlpath = strings.Trim(urlpath, "/")
	if urlpath == "" {
		return nil, fmt.Errorf("ess.read: invalid format: %q", urlpath)
	}

	secretURL, err := url.Parse(urlpath)
	if err != nil {
		return nil, err
	}

	return &VaultPKIQuery{
		stopCh:   make(chan struct{}, 1),
		sleepCh:  make(chan time.Duration, 1),
		pkiPath:  secretURL.Path,
		data:     data,
		filePath: filepath,
	}, nil
}

// Fetch queries the Vault API
func (d *VaultPKIQuery) Fetch(clients *ClientSet, opts *QueryOptions) (interface{}, *ResponseMetadata, error) {
	select {
	case <-d.stopCh:
		return nil, nil, ErrStopped
	default:
	}
	select {
	case dur := <-d.sleepCh:
		// added for nv
		// log sleep duration before next renewal
		if !d.nv.runOnce {
			log.Printf("[INFO] %s next renewal in %q", d.String(), dur)
		}

		time.Sleep(dur)
	default:
	}

	needsRenewal := fmt.Errorf("needs renewal")
	getPEMs := func(renew bool) (PemEncoded, error) {
		rawPems, err := os.ReadFile(d.filePath)
		if renew || err != nil || len(rawPems) == 0 {
			rawPems, err = d.fetchPEMs(clients)
			// no need to write cert to file as it is the template dest
		}
		if err != nil {
			return PemEncoded{}, err
		}

		encPems, cert, err := pemsCert(rawPems)
		if err != nil {
			return encPems, err
		}

		if sleepFor, ok := goodFor(cert); ok {
			d.sleepCh <- sleepFor
			return encPems, nil
		}
		return encPems, needsRenewal
	}

	encPems, err := getPEMs(false)
	switch err {
	case nil:
	case needsRenewal:
		encPems, err = getPEMs(true)
		if err != nil {
			return PemEncoded{}, nil, err
		}
	default:
		return PemEncoded{}, nil, err
	}
	return respWithMetadata(encPems)
}

// added for nv
// returns time left in ~80% w/ a 2% jitter (between 78% and 82%) of the original lease and a boolean
//
// returns time left in ~90% of the original lease and a boolean
// that returns false if cert needs renewing, true otherwise
func goodFor(cert *x509.Certificate) (time.Duration, bool) {
	// If we got called with a cert that doesn't exist, just say there's no
	// time left, and it needs to be renewed
	if cert == nil {
		return 0, false
	}
	// These are all int64's with Seconds since the Epoch, handy for the math
	start, end := cert.NotBefore.Unix(), cert.NotAfter.Unix()
	now := time.Now().UTC().Unix()
	if end <= now { // already expired
		return 0, false
	}
	lifespan := end - start // full ttl of cert
	duration := end - now   // duration remaining

	// added for nv
	// use 80% of duration, instead of 90%
	gooddur := (duration * 8) / 10 // 80% of duration

	// added for nv
	// use 20% of lifespan, instead of 10%
	mindur := lifespan / 5 // 20% of lifespan

	if gooddur <= mindur {
		return 0, false // almost expired, get a new one
	}

	// added for nv
	// use `duration >= 100`, instead of `gooddur > 100`
	if duration >= 100 { // 100 seconds
		// add jitter if big enough for it to matter
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		// added for nv
		// change to between 78% and 82%, instead of 87% and 93%
		// additionally, use floats to calculate sleep duration w/ jitter; this is to avoid having the sleep duration rounded to the nearest whole percent.
		gooddurFloat := float64(gooddur) + (((r.Float64() * pkiLeaseJitterPercentage * 2) - pkiLeaseJitterPercentage) * float64(duration) * 0.01)
		gooddur = int64(gooddurFloat)
	}

	// added for nv
	// use time.Second, instead of 1e9
	sleepFor := time.Duration(gooddur) * time.Second
	return sleepFor, true
}

// loops through all pem encoded blocks in the byte stream
// returning the CA, Certificate and Private Key PEM strings
// also returns the cert for the Certificate as we have it and need it
func pemsCert(encoded []byte) (PemEncoded, *x509.Certificate, error) {
	var block *pem.Block
	var cert *x509.Certificate
	var encPems PemEncoded
	var aPem []byte
	for {
		aPem, encoded = nextPem(encoded)
		// scan, find and parse PEM blocks
		block, _ = pem.Decode(aPem)
		switch {
		case block == nil: // end of scan, no more PEMs found
			return encPems, cert, nil
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			// PKCS#1 and PKCS#8 matching to find private key
			encPems.Key = string(pem.EncodeToMemory(block))
			continue
		}
		// CERTIFICATE PEM blocks (Cert and CA) are left
		maybeCert, err := x509.ParseCertificate(block.Bytes)
		switch {
		case err != nil:
			return PemEncoded{}, nil, err
		case maybeCert.IsCA:
			encPems.CA = string(pem.EncodeToMemory(block))
		default: // the certificate
			cert = maybeCert
			encPems.Cert = string(pem.EncodeToMemory(block))
		}
	}
}

// find the next PEM in the byte stream
func nextPem(encoded []byte) (aPem []byte, theRest []byte) {
	start := bytes.Index(encoded, []byte("-----BEGIN"))
	if start >= 0 { // finds the PEM and pulls it to decode
		encoded = encoded[start:] // clip pre-pem junk
		// find the end
		end := bytes.Index(encoded, []byte("-----END")) + 8
		end = end + bytes.Index(encoded[end:], []byte("-----")) + 5
		// the PEM padded with newlines (what pem.Decode likes)
		aPem = append([]byte("\n"), encoded[:end]...)
		aPem = append(aPem, []byte("\n")...)
		theRest = encoded[end:] // the rest
	}
	return aPem, theRest
}

// Vault call to fetch the PKI Cert PEM data
func (d *VaultPKIQuery) fetchPEMs(clients *ClientSet) ([]byte, error) {
	vaultSecret, err := clients.Vault().Logical().Write(d.pkiPath, d.data)
	switch {
	case err != nil:
		return nil, shimNvClientError(d.nv, d.pkiPath, errors.Wrap(err, d.String()))
	case vaultSecret == nil:
		// added for nv
		// use NewNvNoSecretAtPathError() wrapper error
		return nil, shimNvClientError(d.nv, d.pkiPath, NewNvNoSecretAtPathError(d.pkiPath, d.nv.errorOnMissingKeyEnabled))
	}
	printVaultWarnings(d, vaultSecret.Warnings)
	pems := bytes.Buffer{}
	for _, v := range vaultSecret.Data {
		switch v := v.(type) {
		case string:
			pems.WriteString(v + "\n")
		}
	}

	return pems.Bytes(), nil
}

func (d *VaultPKIQuery) stopChan() chan struct{} {
	return d.stopCh
}

// CanShare returns if this dependency is shareable.
func (d *VaultPKIQuery) CanShare() bool {
	return false
}

// Stop halts the given dependency's fetch.
func (d *VaultPKIQuery) Stop() {
	close(d.stopCh)
}

// String returns the human-friendly version of this dependency.
func (d *VaultPKIQuery) String() string {
	return fmt.Sprintf("ess.pki(%s->%s)", d.pkiPath, d.filePath)
}

// Type returns the type of this dependency.
func (d *VaultPKIQuery) Type() Type {
	return TypeVault
}

// added for nv
// IsRunOnce determines if the query should only fetch secrets once
func (d *VaultPKIQuery) IsRunOnce() bool {
	return d.nv.runOnce
}

// added for nv
// TemplateID captures the template associated with the dependency
func (d *VaultPKIQuery) TemplateID() string {
	return d.nv.templateID
}
