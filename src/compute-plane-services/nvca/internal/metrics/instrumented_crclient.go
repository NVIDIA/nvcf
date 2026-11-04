/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package metrics

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type instrumentedCRClient struct {
	inner client.Client
}

func NewInstrumentedCRClient(client client.Client) client.Client {
	return &instrumentedCRClient{inner: client}
}

func (c *instrumentedCRClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.inner.Get(ctx, key, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := c.inner.List(ctx, list, opts...)
	recordK8sAPICall(ctx, list, err)
	return err
}

func (c *instrumentedCRClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	err := c.inner.Create(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	err := c.inner.Update(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	err := c.inner.Delete(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	err := c.inner.Patch(ctx, obj, patch, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	err := c.inner.DeleteAllOf(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	err := c.inner.Apply(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err)
	return err
}

func (c *instrumentedCRClient) Status() client.StatusWriter { return c.SubResource("status") }

func (c *instrumentedCRClient) SubResource(subResource string) client.SubResourceClient {
	return &instrumentedCRSubresourceClient{
		inner:       c.inner.SubResource(subResource),
		subResource: subResource,
	}
}

func (c *instrumentedCRClient) Scheme() *runtime.Scheme {
	return c.inner.Scheme()
}

func (c *instrumentedCRClient) RESTMapper() meta.RESTMapper {
	return c.inner.RESTMapper()
}

func (c *instrumentedCRClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.inner.GroupVersionKindFor(obj)
}

func (c *instrumentedCRClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return c.inner.IsObjectNamespaced(obj)
}

type instrumentedCRSubresourceClient struct {
	inner       client.SubResourceClient
	subResource string
}

func (c *instrumentedCRSubresourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object,
	opts ...client.SubResourceCreateOption) error {
	err := c.inner.Create(ctx, obj, subResource, opts...)
	recordK8sAPICall(ctx, obj, err, c.subResource)
	return err
}

func (c *instrumentedCRSubresourceClient) Update(ctx context.Context, obj client.Object,
	opts ...client.SubResourceUpdateOption) error {
	err := c.inner.Update(ctx, obj, opts...)
	recordK8sAPICall(ctx, obj, err, c.subResource)
	return err
}

func (c *instrumentedCRSubresourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch,
	opts ...client.SubResourcePatchOption) error {
	err := c.inner.Patch(ctx, obj, patch, opts...)
	recordK8sAPICall(ctx, obj, err, c.subResource)
	return err
}

func (c *instrumentedCRSubresourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object,
	opts ...client.SubResourceGetOption) error {
	err := c.inner.Get(ctx, obj, subResource, opts...)
	recordK8sAPICall(ctx, obj, err, c.subResource)
	return err
}

func recordK8sAPICall(ctx context.Context, obj any, clientErr error, subResource ...string) {
	m := FromContext(ctx)
	if m == nil {
		return
	}
	m.TrackK8sAPICall(getResourceName(obj, subResource...), clientErr)
}

type applyConfiguration interface {
	GetName() *string
	GetNamespace() *string
	GetKind() *string
	GetAPIVersion() *string
}

func getResourceName(o any, subResource ...string) string {
	var resource string
	switch obj := o.(type) {
	case runtime.Object:
		gvk := obj.GetObjectKind().GroupVersionKind()
		kind := gvk.Kind
		if kind == "" {
			typeStr := fmt.Sprintf("%T", obj)
			kind = typeStr[strings.LastIndex(typeStr, ".")+1:]
		}
		if strings.HasSuffix(kind, "List") {
			resource = strings.ToLower(kind[:len(kind)-4])
		} else {
			resource = strings.ToLower(kind)
		}
	case applyConfiguration:
		kindPtr := obj.GetKind()
		var kind string
		if kindPtr == nil {
			typeStr := fmt.Sprintf("%T", o)
			kind = typeStr[strings.LastIndex(typeStr, ".")+1:]
		} else {
			kind = *kindPtr
		}
		if strings.HasSuffix(kind, "ApplyConfiguration") {
			resource = strings.ToLower(kind[:len(kind)-17])
		} else {
			resource = strings.ToLower(kind)
		}
	default:
		resource = "generic"
	}
	if len(subResource) > 0 {
		resource = fmt.Sprintf("%s.%s", resource, strings.ToLower(subResource[0]))
	}
	return resource
}
