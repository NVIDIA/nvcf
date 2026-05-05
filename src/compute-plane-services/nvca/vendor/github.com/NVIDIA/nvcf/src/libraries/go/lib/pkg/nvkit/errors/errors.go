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

package errors

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/api/errors/v1"
)

var (
	ErrBadConfig                 = fmt.Errorf("bad config")
	ErrInvalidConfig             = fmt.Errorf("invalid config")
	ErrUninitializedConfig       = fmt.Errorf("unintialized config")
	ErrBadCerts                  = fmt.Errorf("bad certs")
	ErrCertAndKeyRequired        = fmt.Errorf("missing cert or key file when tls is enabled")
	ErrCertOrKeyFileMissing      = fmt.Errorf("either both tls cert path and tls key path should be provided or none")
	ErrCACertRequired            = fmt.Errorf("client-ca-cert-file must be provided")
	ErrBadTokenConfig            = fmt.Errorf("bad token configuration")
	ErrJwtMissing                = fmt.Errorf("missing jwt token")
	ErrJwtMissingKID             = fmt.Errorf("jwt token missing kid header")
	ErrJwtWrongKey               = fmt.Errorf("jwt key not found")
	ErrFetchingToken             = fmt.Errorf("error fetching token")
	ErrPermissionDenied          = fmt.Errorf("permission denied")
	ErrExceedMaxAllowedRetries   = fmt.Errorf("exceed max allowed retries")
	ErrFailedToFetchJWK          = fmt.Errorf("failed to fetch jwk")
	ErrEmptyEventsList           = fmt.Errorf("the events list is empty")
	ErrEventsSizeExceedsLimit    = fmt.Errorf("the total events size exceeds 1MB")
	ErrNegativeValue             = fmt.Errorf("the value must not be negative")
	ErrNonPositiveValue          = fmt.Errorf("the value must  be positive")
	ErrExceedMaxBufferSize       = fmt.Errorf("the size of the adding events exceeds max buffer size")
	ErrExceedMaxEventsListLength = fmt.Errorf("the size of the adding events exceeds max event list length")
	ErrEmptyCollectorID          = fmt.Errorf("invalid metering config, collector id can not be empty")
)

type ConfigError struct {
	FieldName string
	Message   string
}

func (c *ConfigError) Error() string {
	fieldInfo := ""
	if c.FieldName != "" {
		fieldInfo = fmt.Sprintf("field: %s ", c.FieldName)
	}
	return fmt.Sprintf("%smessage: %s", fieldInfo, c.Message)
}

type Error interface {
	error

	WithCode(codes.Code) Error
	WithReason(string) Error
	WithOrigin(string) Error
	WithMetadata(md map[string]string) Error

	ID() string
	Origin() string
	Reason() string
	Metadata() map[string]string
	GetMetadata(key string) string
	GRPCStatus() *status.Status
}

type NVError struct {
	nverrors.NVError
	code codes.Code
}

func NewNVError(err error) Error {
	var nvErr *NVError
	if errors.As(err, &nvErr) {
		return nvErr
	}
	return &NVError{
		NVError: nverrors.NVError{
			ErrorId: uuid.New().String(),
			Reason:  err.Error(),
		},
		code: codes.Unknown,
	}
}

func (e *NVError) Error() string { return e.NVError.String() } //nolint:staticcheck // QF1008: explicit selector for clarity

func (e *NVError) GRPCStatus() *status.Status {
	grpcErr := status.New(e.code, e.Reason())
	grpcErrWithDetails, err := grpcErr.WithDetails(&e.NVError)
	if err != nil {
		return grpcErr
	}
	return grpcErrWithDetails
}

func (e *NVError) WithCode(code codes.Code) Error {
	e.code = code
	return e
}

func (e *NVError) WithReason(reason string) Error {
	e.NVError.Reason = reason
	return e
}

func (e *NVError) WithOrigin(origin string) Error {
	e.NVError.Origin = origin
	return e
}

func (e *NVError) WithMetadata(md map[string]string) Error {
	if e.NVError.Metadata == nil {
		e.NVError.Metadata = map[string]string{}
	}
	errMetadata := e.NVError.Metadata
	for k, v := range md {
		errMetadata[k] = v
	}
	return e
}

func (e *NVError) Origin() string                { return e.NVError.Origin }
func (e *NVError) Reason() string                { return e.NVError.Reason }
func (e *NVError) ID() string                    { return e.NVError.ErrorId } //nolint:staticcheck // QF1008: explicit selector for clarity
func (e *NVError) Metadata() map[string]string   { return e.NVError.Metadata }
func (e *NVError) GetMetadata(key string) string { return e.NVError.Metadata[key] }

// NVErrorMarshaler implements runtime.Marshaler.
// It finds if the error is of type NVError and marshals it to NVError type
// rather than the standard grpc_status.Status type
type NVErrorMarshaler struct {
	FallbackMarshaler runtime.Marshaler
}

// Marshal overrides the format for NVError.
// If it's not NVError type, it falls back to standard definition.
func (c *NVErrorMarshaler) Marshal(v interface{}) ([]byte, error) {
	s, ok := v.(*spb.Status)
	if ok {
		status := status.FromProto(s)
		for _, errDetails := range status.Details() {
			switch info := errDetails.(type) { //nolint:gocritic
			case *nverrors.NVError:
				jsonMarshaler := &runtime.JSONPb{}
				return jsonMarshaler.Marshal(info)
			}
		}
	}
	return c.FallbackMarshaler.Marshal(v)
}
func (c *NVErrorMarshaler) Unmarshal(data []byte, v interface{}) error {
	return c.FallbackMarshaler.Unmarshal(data, v)
}
func (c *NVErrorMarshaler) NewDecoder(r io.Reader) runtime.Decoder {
	return c.FallbackMarshaler.NewDecoder(r)
}
func (c *NVErrorMarshaler) NewEncoder(w io.Writer) runtime.Encoder {
	return c.FallbackMarshaler.NewEncoder(w)
}
func (c *NVErrorMarshaler) ContentType(v interface{}) string {
	return c.FallbackMarshaler.ContentType(v)
}

// HttpStatusCodeToGrpcCode takes an int of a httpStatus and returns the closest grpc code
func HttpStatusCodeToGrpcCode(code int) codes.Code {
	switch code {
	case http.StatusOK:
		return codes.OK
	case http.StatusRequestTimeout:
		return codes.Internal
	case http.StatusBadRequest:
		return codes.InvalidArgument
	case http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusConflict:
		return codes.AlreadyExists
	case http.StatusUnprocessableEntity:
		return codes.OutOfRange
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusInternalServerError:
		return codes.Internal
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	}
	return codes.Internal
}
