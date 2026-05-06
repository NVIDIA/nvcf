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

package reval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/reval/cache"
)

type Handler struct {
	HandlerOptions

	Logger      *zap.Logger
	ServiceName string

	httpClient      *http.Client
	newImageChecker func() imageChecker
	// Image cache should be shared between threads.
	imageCache cache.ImageCache
	serializer *serializerImpl
}

type HandlerOptions struct {
	// Skip validation of objects.
	SkipValidateObjects bool
	// Skip validation of images.
	SkipValidateImages bool
	// Configured labels to preserve
	PreserveLabels []string
	// Configured annotations to preserve
	PreserveAnnotations []string
}

type Config struct {
	Render      bool
	ChartURL    string
	ReleaseName string
	Namespace   string
	Values      json.RawMessage
	K8sVersion  string
	APIVersions []string
	// TargetServiceName is the name of the service that inference requests
	// will be served from. A service with this name must exist,
	// and ports must match those on some pod spec.
	TargetServiceName string
	// TargetServicePort is the inference and health port on the service
	// whose name is provided as part of Helm Chart function creation.
	TargetServicePort int32
	// TargetHTTPHealthEndpoint is the HTTP health endpoint that is used
	// for function healthchecks, ex. /v1/livez.
	TargetHTTPHealthEndpoint string
	// NGCAPIKey is the legacy API key for NGC helm auth.
	// Only use if HelmRegistryAuthConfig is not set.
	NGCAPIKey string
	// HelmRegistryAuthConfig configures how reval authenticates with Helm registry servers.
	HelmRegistryAuthConfig common.RegistryAuthConfig
	// ImageRegistryAuthConfig configures how reval authenticates with image registry servers.
	ImageRegistryAuthConfig common.RegistryAuthConfig
	// RenderPolicy defines which validation rules and types are applied to a helm chart for rendering.
	RenderPolicy ValidationPolicy
	// ValidatePolicies defines multiple sets validation rules and types are applied to a helm chart.
	ValidatePolicies []ValidationPolicy
}

type ValidationPolicy struct {
	ID        string
	Name      PolicyName
	ExtraGVKs []schema.GroupVersionKind
}

type PolicyName string

const (
	DefaultPolicy      PolicyName = "Default"
	UnrestrictedPolicy PolicyName = "Unrestricted"
)

type Result struct {
	Valid              bool                     `json:"valid"`
	ValidationErrors   []error                  `json:"validationErrors,omitempty"`
	ValidationPolicies []ValidationPolicyResult `json:"validationPolicies,omitempty"`
}

type ValidationPolicyResult struct {
	ID               string  `json:"id"`
	Valid            bool    `json:"valid"`
	ValidationErrors []error `json:"validationErrors,omitempty"`
}

type logContextValue struct{}

func logIntoContext(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, logContextValue{}, l)
}

func logFromContext(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(logContextValue{}).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.NewNop()
}

func NewHandler(
	logger *zap.Logger,
	serviceName string,
	opts HandlerOptions,
) (*Handler, error) {
	h := &Handler{
		Logger:         logger,
		ServiceName:    serviceName,
		HandlerOptions: opts,
	}

	trpt := http.DefaultTransport.(*http.Transport).Clone()
	if plainHTTP {
		trpt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	h.httpClient = &http.Client{
		Transport: retry.NewTransport(trpt),
	}
	h.imageCache = cache.NewImageCache(h.Logger)
	h.newImageChecker = func() imageChecker {
		return remoteImageChecker{
			newImageClient: func(credsStore credentials.Store) orasauth.Client {
				return newORASClient(h.httpClient, credsStore)
			},
			credHelper: credhelper.NewCredHelper(),
			imageCache: h.imageCache,
		}
	}
	slzr, err := newSerializer()
	if err != nil {
		return nil, err
	}
	h.serializer = slzr

	return h, nil
}

func (h *Handler) Run(ctx context.Context, cfg Config, out io.Writer) (result Result, err error) {
	ctx, span := otel.Tracer(h.ServiceName).Start(
		ctx,
		"reval.handler.run",
		trace.WithAttributes(attribute.String("helmChart", cfg.ChartURL)),
	)
	defer span.End()

	timer := metrics.RunTimerFromContext(ctx)
	timer.RecordThreadStart()
	defer timer.RecordThreadEnd()

	logger := zapotelspan.ContextLogger(ctx, h.Logger).With(
		zap.String("helm_chart", cfg.ChartURL))
	ctx = logIntoContext(ctx, logger)
	defer func() {
		logger.Info("reval run finished",
			zap.Bool("valid", result.Valid),
			zap.Errors("validation_errors", result.ValidationErrors),
		)
	}()

	var fields []zap.Field
	if logger.Level().Enabled(zap.DebugLevel) {
		fields = []zap.Field{zap.String("config", configForLogging(cfg))}
	}

	logger.Info("Running ReVal handler", fields...)

	if errs := validateInputs(cfg); len(errs) != 0 {
		return Result{
			ValidationErrors: errs,
		}, nil
	}

	settings, workingDir, err := initHelmEnv()
	if err != nil {
		return Result{}, err
	}
	settings.Debug = logger.Core().Enabled(zap.DebugLevel)
	settings.SetNamespace(cfg.Namespace)

	// Ensure state is cleaned up, even in case of panic.
	defer func() {
		err := recover()
		os.RemoveAll(workingDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Panic caught\n%v\n", err)
		}
	}()

	helmOut := &bytes.Buffer{}
	if err := h.runHelmTemplate(ctx, cfg, settings, workingDir, helmOut); err != nil {
		if _, err := io.Copy(os.Stderr, helmOut); err != nil {
			logger.Error("Copy error output", zap.Error(err))
		}
		span.RecordError(err)
		return Result{
			ValidationErrors: []error{fmt.Errorf("run helm template: %v", err)},
		}, nil
	}

	inObjs, derrs, err := h.serializer.decode(logger, helmOut)
	if err != nil || len(derrs) != 0 {
		if err != nil {
			return Result{}, fmt.Errorf("failed to decode rendered output: %v", err)
		}
		return Result{
			ValidationErrors: derrs,
		}, nil
	}

	svTraceCtx, svSpan := otel.Tracer(h.ServiceName).Start(
		ctx,
		"reval.handler.validateRelease",
		trace.WithAttributes(attribute.String("helmChart", cfg.ChartURL)),
	)
	defer svSpan.End()

	if cfg.Render {
		result, err = h.runRender(svTraceCtx, logger, cfg, inObjs, out)
	} else {
		result, err = h.runValidate(svTraceCtx, logger, cfg, inObjs)
	}

	return result, err
}

func (h *Handler) runValidate(
	ctx context.Context,
	logger *zap.Logger,
	cfg Config,
	inObjs []runtime.Object,
) (result Result, err error) {
	if len(cfg.ValidatePolicies) == 0 {
		if err := checkTypes(logger, inObjs); err != nil {
			result.ValidationErrors = []error{err}
			return result, nil
		}

		_, valErrs, err := h.validateRenderedRelease(ctx, logger, cfg, ValidationPolicy{}, inObjs)
		if err != nil {
			return Result{}, err
		}
		if len(valErrs) != 0 {
			logger.Info("Found errors")

			result.ValidationErrors = valErrs
			sortErrors(result.ValidationErrors)
		} else {
			logger.Info("Rendered release is validated and sanitized")

			result.Valid = true
		}
		return result, nil
	}

	anyValid := false
	for _, vp := range cfg.ValidatePolicies {
		vpr := ValidationPolicyResult{
			ID: vp.ID,
		}
		if err := checkTypes(logger, inObjs, vp.ExtraGVKs...); err != nil {
			vpr.ValidationErrors = []error{err}
			result.ValidationPolicies = append(result.ValidationPolicies, vpr)
			continue
		}

		var valErrs []error
		if vp.Name == "" || vp.Name == DefaultPolicy {
			_, valErrs, err = h.validateRenderedRelease(ctx, logger, cfg, vp, inObjs)
			if err != nil {
				return Result{}, err
			}
		}
		if len(valErrs) != 0 {
			vpr.ValidationErrors = valErrs
			sortErrors(vpr.ValidationErrors)
		} else {
			anyValid = true
			vpr.Valid = true
		}

		result.ValidationPolicies = append(result.ValidationPolicies, vpr)
	}

	if anyValid {
		logger.Info("Rendered release is validated and sanitized")
		result.Valid = true
	} else {
		logger.Info("Found errors")
	}

	return result, nil
}

func (h *Handler) runRender(
	ctx context.Context,
	logger *zap.Logger,
	cfg Config,
	inObjs []runtime.Object,
	out io.Writer,
) (result Result, err error) {
	if err := checkTypes(logger, inObjs, cfg.RenderPolicy.ExtraGVKs...); err != nil {
		return Result{
			Valid:            false,
			ValidationErrors: []error{err},
		}, nil
	}
	var (
		outObjs []runtime.Object
		valErrs []error
	)
	if cfg.RenderPolicy.Name == "" || cfg.RenderPolicy.Name == DefaultPolicy {
		outObjs, valErrs, err = h.validateRenderedRelease(ctx, logger, cfg, cfg.RenderPolicy, inObjs)
		if err != nil {
			return Result{}, err
		}
	} else {
		// Unrestricted case.
		outObjs = inObjs
	}
	if len(valErrs) != 0 {
		logger.Info("Found errors")

		result.ValidationErrors = valErrs
		sortErrors(result.ValidationErrors)
	} else {
		logger.Info("Rendered release is validated and sanitized")

		if out != nil {
			logger.Debug("Rendering objects to output")
			if err := h.serializer.encode(out, outObjs); err != nil {
				return Result{}, err
			}
		}
		result.Valid = true
	}

	return
}

func sortErrors(errs []error) {
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
}

func validateInputs(cfg Config) (errs []error) {
	errs = append(errs, validateHelmChartURL(cfg.ChartURL)...)
	if cfg.K8sVersion != "" {
		if _, err := chartutil.ParseKubeVersion(cfg.K8sVersion); err != nil {
			errs = append(errs, fmt.Errorf("invalid kube version %q: %s", cfg.K8sVersion, err))
		}
	}
	return errs
}

func validateHelmChartURL(urlStr string) (errs []error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		errs = append(errs, fmt.Errorf("invalid helm chart URL: %v", err))
		return
	}

	if u.Scheme == "" || u.Scheme == "file" || u.Host == "" {
		errs = append(errs, fmt.Errorf("helm chart URL must be absolute, got %q", urlStr))
	}

	return errs
}

// validateRenderedRelease will read a maximum of 100Mb YAML (1Mb per k8s object, 100 objects)
// from in into out as JSON.
func (h *Handler) validateRenderedRelease(
	ctx context.Context,
	logger *zap.Logger,
	cfg Config,
	vp ValidationPolicy,
	inObjs []runtime.Object,
) (objs []runtime.Object, valErrs []error, err error) { //nolint:gocyclo
	vTraceCtx, validateSpan := otel.Tracer(h.ServiceName).Start(ctx, "reval.handler.validateRelease")
	defer validateSpan.End()

	logger.Info("Sanitizing objects")

	matcherSet := map[client.Object]matcher{}
	matchTargetSet := map[client.Object]matchTarget{}

	objs = inObjs

	// Match objects that select a pod to pod or dep/repset/ss by selectors
	// to later generate random labels and their selectors.
	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.Pod:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Labels),
			}
		case *appsv1.Deployment:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Spec.Template.Labels),
			}
		case *appsv1.ReplicaSet:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Spec.Template.Labels),
			}
		case *appsv1.StatefulSet:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Spec.Template.Labels),
			}
		case *batchv1.Job:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Spec.Template.Labels),
			}
		case *batchv1.CronJob:
			matchTargetSet[t] = matchTarget{
				labels: labels.Set(t.Spec.JobTemplate.Spec.Template.Labels),
			}
		case *corev1.Service:
			sel := make(map[string]string, len(t.Spec.Selector))
			maps.Copy(sel, t.Spec.Selector)
			m := matcher{}
			// Handle the pod-index label, which will not be generated until deploy-time.
			// The match must be against a StatefulSet if present.
			if t.Spec.Selector != nil && t.Spec.Selector[podIndexLabel] != "" {
				delete(sel, podIndexLabel)
				m.objType = reflect.TypeOf(&appsv1.StatefulSet{})
			}
			m.selector = labels.SelectorFromSet(sel)
			matcherSet[t] = m
		}
	}

	// If a target service name was specified, it must be found in objects.
	var targetService *corev1.Service
	for _, obj := range objs {
		a, err := meta.Accessor(obj)
		if err != nil {
			valErrs = append(valErrs, err)
			continue
		}
		sanitizeObjectMeta(a, h.PreserveLabels, h.PreserveAnnotations)
		switch t := obj.(type) {
		case *corev1.Pod:
			sanitizePodSpec(&t.Spec)
		case *appsv1.Deployment:
			sanitizePodSpec(&t.Spec.Template.Spec)
			key, val, err := generateRandomLabelKV("controller")
			if err != nil {
				return nil, nil, fmt.Errorf("gen randomized label for %T: %v", t, err)
			}
			t.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: map[string]string{key: val},
			}
			t.Spec.Template.Labels = map[string]string{key: val}
		case *appsv1.ReplicaSet:
			sanitizePodSpec(&t.Spec.Template.Spec)
			key, val, err := generateRandomLabelKV("controller")
			if err != nil {
				return nil, nil, fmt.Errorf("gen randomized label for %T: %v", t, err)
			}
			t.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: map[string]string{key: val},
			}
			t.Spec.Template.Labels = map[string]string{key: val}
		case *appsv1.StatefulSet:
			sanitizePodSpec(&t.Spec.Template.Spec)
			key, val, err := generateRandomLabelKV("controller")
			if err != nil {
				return nil, nil, fmt.Errorf("gen randomized label for %T: %v", t, err)
			}
			t.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: map[string]string{key: val},
			}
			t.Spec.Template.Labels = map[string]string{key: val}
		case *batchv1.CronJob:
			sanitizePodSpec(&t.Spec.JobTemplate.Spec.Template.Spec)
			// Jobs generated by CronJobs will have selectors/labels generated automatically.
			// Disallow manual selection.
			t.Spec.JobTemplate.Spec.Selector = nil
			t.Spec.JobTemplate.Spec.ManualSelector = nil
			t.Spec.JobTemplate.Spec.Template.Labels = nil
		case *batchv1.Job:
			sanitizePodSpec(&t.Spec.Template.Spec)
		case *corev1.Service:
			if t.Name == cfg.TargetServiceName {
				targetService = t
			}
			sanitizeServiceSpec(&t.Spec, h.PreserveLabels)
		}
	}

	logger.Info("Applying randomized selectors to relevant objects")

	selKeys := getSortedObjectKeys(matcherSet)

	for _, selObj := range selKeys {
		m := matcherSet[selObj]
		// Use short name hash to avoid label key collision.
		dig := sha256.Sum256([]byte(selObj.GetName()))
		shortHash := hex.EncodeToString(dig[:])[:10]

		var (
			key, val string
			err      error
		)
		switch t := selObj.(type) {
		case *corev1.Service:
			suffix := fmt.Sprintf("service-%s", shortHash)
			key, val, err = generateRandomLabelKV(suffix)
			if err != nil {
				return nil, nil, fmt.Errorf("gen randomized label for %T: %v", t, err)
			}
			if t.Spec.Selector == nil {
				t.Spec.Selector = map[string]string{}
			}
			t.Spec.Selector[key] = val
		default:
			continue
		}
		for matchObj, mt := range matchTargetSet {
			if !m.matches(matchObj, mt) {
				continue
			}
			switch t := matchObj.(type) {
			case *corev1.Pod:
				if t.Labels == nil {
					t.Labels = map[string]string{}
				}
				t.Labels[key] = val
			case *appsv1.Deployment:
				if t.Spec.Template.Labels == nil {
					t.Spec.Template.Labels = map[string]string{}
				}
				t.Spec.Template.Labels[key] = val
			case *appsv1.ReplicaSet:
				if t.Spec.Template.Labels == nil {
					t.Spec.Template.Labels = map[string]string{}
				}
				t.Spec.Template.Labels[key] = val
			case *appsv1.StatefulSet:
				if t.Spec.Template.Labels == nil {
					t.Spec.Template.Labels = map[string]string{}
				}
				t.Spec.Template.Labels[key] = val
			case *batchv1.CronJob:
				if t.Spec.JobTemplate.Spec.Template.Labels == nil {
					t.Spec.JobTemplate.Spec.Template.Labels = map[string]string{}
				}
				t.Spec.JobTemplate.Spec.Template.Labels[key] = val
			case *batchv1.Job:
				if t.Spec.Template.Labels == nil {
					t.Spec.Template.Labels = map[string]string{}
				}
				t.Spec.Template.Labels[key] = val
			}
		}
	}

	logger.Info("Validating objects")
	verrs := h.validateReleaseObjects(vTraceCtx, logger, cfg, vp, targetService, matcherSet, matchTargetSet, objs)
	if h.SkipValidateObjects {
		if len(verrs) != 0 {
			logger.Info("Release is sanitized and image validation errors found but validation is skipped")
		}
		for _, err := range verrs {
			logger.Error("Object validation error", zap.Error(err))
			validateSpan.RecordError(err)
		}
	} else {
		valErrs = append(valErrs, verrs...)
	}

	logger.Info("Validating object images")
	extraObjs, ierrs := h.validateReleaseImages(vTraceCtx, logger, cfg, objs)
	objs = append(objs, extraObjs...)
	if h.SkipValidateImages {
		if len(ierrs) != 0 {
			logger.Info("Release is sanitized and image validation errors found but validation is skipped")
		}
		for _, err := range ierrs {
			logger.Error("Image validation error", zap.Error(err))
			validateSpan.RecordError(err)
		}
	} else {
		valErrs = append(valErrs, ierrs...)
	}

	// For span filtering.
	validateSpan.SetAttributes(attribute.Bool("isValid", len(verrs) == 0 && len(ierrs) == 0))

	return objs, valErrs, nil
}

func hasTypeMeta(b []byte) (bool, error) {
	var tm metav1.TypeMeta
	err := yaml.Unmarshal(b, &tm)
	return err == nil && tm.APIVersion != "" && tm.Kind != "", err
}

func createTypeString(obj runtime.Object) string {
	if gvk := obj.GetObjectKind().GroupVersionKind(); gvk != (schema.GroupVersionKind{}) {
		if gvk.Group == "" && gvk.Version == "v1" {
			gvk.Group = "core"
		}
		return fmt.Sprintf("%s/%s.%s", gvk.Group, gvk.Version, gvk.Kind)
	}
	return fmt.Sprintf("%T", obj)
}

func (h *Handler) validateReleaseObjects(
	_ context.Context,
	logger *zap.Logger,
	cfg Config,
	vp ValidationPolicy,
	targetService *corev1.Service,
	matcherSet map[client.Object]matcher,
	matchTargetSet map[client.Object]matchTarget,
	objs []runtime.Object,
) (errs []error) {

	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.Pod:
			errs = append(errs, validatePodSpec(t.Spec)...)
		case *appsv1.Deployment:
			errs = append(errs, validatePodSpec(t.Spec.Template.Spec)...)
		case *appsv1.ReplicaSet:
			errs = append(errs, validatePodSpec(t.Spec.Template.Spec)...)
		case *appsv1.StatefulSet:
			errs = append(errs, validatePodSpec(t.Spec.Template.Spec)...)
		case *batchv1.CronJob:
			errs = append(errs, validatePodSpec(t.Spec.JobTemplate.Spec.Template.Spec)...)
		case *batchv1.Job:
			errs = append(errs, validatePodSpec(t.Spec.Template.Spec)...)
		case *corev1.Service:
			errs = append(errs, validateServiceSpec(t.Spec)...)
			if t.Name == cfg.TargetServiceName {
				if err := validateServicePortMatch(t, cfg.TargetServicePort); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}

	if targetService == nil && cfg.TargetServiceName != "" {
		if len(vp.ExtraGVKs) == 0 {
			errs = append(errs, fmt.Errorf("helm chart service name %q not found", cfg.TargetServiceName))
		} else {
			logger.Info("Helm chart service not found but validation policy contains extra GVKs, ignoring error",
				zap.String("service", cfg.TargetServiceName))
		}
	}

	if targetService != nil && len(vp.ExtraGVKs) == 0 {
		foundServicePortOnPodSpec := false
		checkServicePortMatchesSomePodSpec := func(podSpec corev1.PodSpec) {
			if !foundServicePortOnPodSpec {
				foundServicePortOnPodSpec = validateServicePort(
					targetService, cfg.TargetServicePort, podSpec,
				)
			}
		}
		m := matcherSet[targetService]
		for matchObj, mt := range matchTargetSet {
			if !m.matches(matchObj, mt) {
				continue
			}
			switch t := matchObj.(type) {
			case *corev1.Pod:
				checkServicePortMatchesSomePodSpec(t.Spec)
			case *appsv1.Deployment:
				checkServicePortMatchesSomePodSpec(t.Spec.Template.Spec)
			case *appsv1.ReplicaSet:
				checkServicePortMatchesSomePodSpec(t.Spec.Template.Spec)
			case *appsv1.StatefulSet:
				checkServicePortMatchesSomePodSpec(t.Spec.Template.Spec)
			case *batchv1.CronJob:
				checkServicePortMatchesSomePodSpec(t.Spec.JobTemplate.Spec.Template.Spec)
			case *batchv1.Job:
				checkServicePortMatchesSomePodSpec(t.Spec.Template.Spec)
			}
		}

		if !foundServicePortOnPodSpec {
			errs = append(errs, fmt.Errorf("provided helm chart service %q has no port %d matching selected pod specs",
				cfg.TargetServiceName, cfg.TargetServicePort))
		}
	}

	return errs
}

func (h *Handler) validateReleaseImages(
	ctx context.Context,
	_ *zap.Logger,
	cfg Config,
	objs []runtime.Object,
) (extraObjs []runtime.Object, errs []error) {
	ctx, span := otel.Tracer(h.ServiceName).Start(ctx, "reval.handler.validateRelease.images")
	defer span.End()

	timer := metrics.RunTimerFromContext(ctx)
	timer.RecordImageCheckStart()
	defer timer.RecordImageCheckEnd()

	imageTagSet := sets.New[string]()
	for _, obj := range objs {
		switch t := obj.(type) {
		case *corev1.Pod:
			collectImages(imageTagSet, t.Spec)
		case *appsv1.Deployment:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *appsv1.ReplicaSet:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *appsv1.StatefulSet:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		case *batchv1.CronJob:
			collectImages(imageTagSet, t.Spec.JobTemplate.Spec.Template.Spec)
		case *batchv1.Job:
			collectImages(imageTagSet, t.Spec.Template.Spec)
		}
	}

	_, ierrs := validateImages(ctx, h.newImageChecker(), cfg, imageTagSet.UnsortedList())
	if len(ierrs) != 0 {
		errs = append(errs, ierrs...)
	}
	// TODO: enable once NVCA supports validating image pull secrets.
	//
	// for name, secretData := range usedImageSecrets {
	// 	b, err := json.Marshal(secretData)
	// 	if err != nil {
	// 		errs = append(errs, internalError{e: err})
	// 		continue
	// 	}
	// 	secret := &corev1.Secret{}
	// 	secret.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
	// 	secret.Name = name
	// 	secret.Type = corev1.SecretTypeDockerConfigJson
	// 	secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: b}
	// 	extraObjs = append(extraObjs, secret)
	// }

	return extraObjs, errs
}

type matcher struct {
	selector labels.Selector
	objType  reflect.Type
}

type matchTarget struct {
	labels labels.Labels
}

func (m matcher) matches(obj client.Object, mt matchTarget) bool {
	if m.objType != nil && m.objType != reflect.TypeOf(obj) {
		return false
	}
	return m.selector.Matches(mt.labels)
}

func getSortedObjectKeys(s map[client.Object]matcher) []client.Object {
	keys := make([]client.Object, len(s))
	i := 0
	for obj := range s {
		keys[i] = obj
		i++
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].GetName() == keys[j].GetName() {
			iGVK := keys[i].GetObjectKind().GroupVersionKind()
			jGVK := keys[j].GetObjectKind().GroupVersionKind()
			return iGVK.String() < jGVK.String()
		}
		return keys[i].GetName() < keys[j].GetName()
	})
	return keys
}

const (
	// https://kubernetes.io/docs/reference/labels-annotations-taints/#apps-kubernetes.io-pod-index
	podIndexLabel = "apps.kubernetes.io/pod-index"
)

func sanitizeObjectMeta(om metav1.Object, preserveLabels, preserveAnnotations []string) {
	om.SetLabels(getPreservedSet(om.GetLabels(), preserveLabels))
	om.SetAnnotations(getPreservedSet(om.GetAnnotations(), preserveAnnotations))
	om.SetOwnerReferences(nil)
}

func getPreservedSet(original map[string]string, preserve []string) map[string]string {
	if original == nil {
		return original
	}
	newSet := map[string]string{}
	for _, rk := range preserve {
		if rv, ok := original[rk]; ok {
			newSet[rk] = rv
		}
	}
	return newSet
}

func sanitizeServiceSpec(ss *corev1.ServiceSpec, preserveLabels []string) {
	if ss.Selector == nil {
		return
	}
	newSelector := map[string]string{}
	for _, rk := range preserveLabels {
		if rv, ok := ss.Selector[rk]; ok {
			newSelector[rk] = rv
		}
	}
	ss.Selector = newSelector
}

func validateServiceSpec(ss corev1.ServiceSpec) (errs []error) {
	if ss.Type != "" && ss.Type != corev1.ServiceTypeClusterIP {
		errs = append(errs, fmt.Errorf("service is not ClusterIP"))
	}
	return errs
}

func sanitizePodSpec(ps *corev1.PodSpec) {
	if ps.Affinity != nil {
		ps.Affinity.NodeAffinity = nil
		if ps.Affinity.PodAffinity == nil && ps.Affinity.PodAntiAffinity == nil {
			ps.Affinity = nil
		}
	}
	ps.NodeSelector = nil
	ps.NodeName = ""

	if ps.AutomountServiceAccountToken == nil {
		ps.AutomountServiceAccountToken = newBoolPtr(false)
	}

	ps.ServiceAccountName = ""

	ps.PriorityClassName = ""

	for i := range ps.InitContainers {
		ps.InitContainers[i].SecurityContext = sanitizeContainerSecurityContext(ps.InitContainers[i].SecurityContext)
	}

	for i := range ps.Containers {
		ps.Containers[i].SecurityContext = sanitizeContainerSecurityContext(ps.Containers[i].SecurityContext)
	}
}

func sanitizeContainerSecurityContext(sc *corev1.SecurityContext) *corev1.SecurityContext {
	if sc == nil || *sc == (corev1.SecurityContext{}) {
		return sc
	}
	// Only default this if sc is set and privilege escalation is not set or won't default to true.
	if sc.AllowPrivilegeEscalation == nil &&
		(sc.Privileged == nil || !*sc.Privileged) &&
		(sc.Capabilities == nil || !slices.ContainsFunc(sc.Capabilities.Add, func(c corev1.Capability) bool {
			return strings.EqualFold(string(c), "CAP_SYS_ADMIN")
		})) {
		sc.AllowPrivilegeEscalation = newBoolPtr(false)
	}

	return sc
}

func validatePodSpec(ps corev1.PodSpec) (errs []error) {
	for _, c := range append(ps.InitContainers, ps.Containers...) {
		if strings.TrimSpace(c.Image) == "" {
			errs = append(errs, fmt.Errorf("container %q has no image", c.Name))
		}
	}
	errs = append(errs, validatePodSpecSecurityFeatures(ps)...)
	errs = append(errs, validateVolumes(ps.Volumes)...)
	return errs
}

func validatePodSpecSecurityFeatures(ps corev1.PodSpec) (errs []error) {
	if ps.HostIPC {
		errs = append(errs, fmt.Errorf("hostIPC must be false"))
	}
	if ps.HostNetwork {
		errs = append(errs, fmt.Errorf("hostNetwork must be false"))
	}
	if ps.HostPID {
		errs = append(errs, fmt.Errorf("hostPID must be false"))
	}

	if ps.AutomountServiceAccountToken != nil && *ps.AutomountServiceAccountToken {
		errs = append(errs, fmt.Errorf("automountServiceAccountToken must be false"))
	}

	// All containers get a default security context if unset during sanitization,
	// so this code assumes they are all set. It merges the pod's security context's
	// fields into containers' so the pods' doesn't need validating directly.
	for _, c := range ps.InitContainers {
		errs = append(errs, validatePorts(c.Ports)...)

		sc := c.SecurityContext.DeepCopy()
		sc = mergeSecurityContexts(ps.SecurityContext, sc)
		errs = append(errs, validateContainerSecurityContext(sc, fmt.Sprintf("initContainers[\"%s\"]", c.Name))...)
	}
	for _, c := range ps.Containers {
		errs = append(errs, validatePorts(c.Ports)...)

		sc := c.SecurityContext.DeepCopy()
		sc = mergeSecurityContexts(ps.SecurityContext, sc)
		errs = append(errs, validateContainerSecurityContext(sc, fmt.Sprintf("containers[\"%s\"]", c.Name))...)
	}

	return errs
}

func validateContainerSecurityContext(sc *corev1.SecurityContext, errPrefix string) (errs []error) {
	if sc == nil {
		return []error{fmt.Errorf("%s.securityContext must be set", errPrefix)}
	}

	// Container-specific fields.
	if sc.Capabilities == nil {
		errs = append(errs, fmt.Errorf("%s.securityContext.capabilities must be set to drop=[\"ALL\"] and optionally add=[\"NET_BIND_SERVICE\"]", errPrefix))
	} else {
		if !slices.ContainsFunc(sc.Capabilities.Drop, func(c corev1.Capability) bool {
			return strings.EqualFold(string(c), "ALL")
		}) {
			errs = append(errs, fmt.Errorf("%s.securityContext.capabilities.drop must contain \"ALL\"", errPrefix))
		}
		if l := len(sc.Capabilities.Add); l != 0 && (l > 1 || !strings.EqualFold(string(sc.Capabilities.Add[0]), "NET_BIND_SERVICE")) {
			errs = append(errs, fmt.Errorf("%s.securityContext.capabilities.add must be unset or set to [\"NET_BIND_SERVICE\"]", errPrefix))
		}
	}
	if sc.Privileged != nil && *sc.Privileged {
		errs = append(errs, fmt.Errorf("%s.securityContext.privileged must not be true", errPrefix))
	}
	if sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation {
		errs = append(errs, fmt.Errorf("%s.securityContext.allowPrivilegeEscalation must not be true", errPrefix))
	}
	if sc.ProcMount != nil && *sc.ProcMount != corev1.DefaultProcMount {
		errs = append(errs, fmt.Errorf("%s.securityContext.procMount must be unset or set to Default", errPrefix))
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		errs = append(errs, fmt.Errorf("%s.securityContext.readOnlyRootFilesystem must be set to true", errPrefix))
	}
	// Container or Pod-level fields.
	if sc.RunAsUser != nil && *sc.RunAsUser == 0 {
		errs = append(errs, fmt.Errorf("%s.securityContext.runAsUser or podSpec.securityContext.runAsUser must not be 0", errPrefix))
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		errs = append(errs, fmt.Errorf("%s.securityContext.runAsNonRoot or podSpec.securityContext.runAsNonRoot must be set to true", errPrefix))
	}
	if scc := sc.SeccompProfile; scc == nil {
		errs = append(errs, fmt.Errorf("%s.securityContext.seccompProfile or podSpec.securityContext.seccompProfile must have type=RuntimeDefault", errPrefix))
	} else {
		if scc.Type != corev1.SeccompProfileTypeRuntimeDefault && scc.Type != corev1.SeccompProfileTypeLocalhost {
			errs = append(errs, fmt.Errorf("%s.securityContext.seccompProfile.type or podSpec.securityContext.seccompProfile.type must be set to one of RuntimeDefault/Localhost", errPrefix))
		}
	}
	if aa := sc.AppArmorProfile; aa != nil {
		if aa.Type != "" && aa.Type != corev1.AppArmorProfileTypeRuntimeDefault && aa.Type != corev1.AppArmorProfileTypeLocalhost {
			errs = append(errs, fmt.Errorf("%s.securityContext.appArmorProfile.type or podSpec.securityContext.appArmorProfile.type must be set to one of \"\"/RuntimeDefault/Localhost", errPrefix))
		}
	}
	if se := sc.SELinuxOptions; se != nil {
		if se.Type != "" &&
			se.Type != "container_t" &&
			se.Type != "container_init_t" &&
			se.Type != "container_kvm_t" &&
			se.Type != "container_engine_t" {
			errs = append(errs, fmt.Errorf("%s.securityContext.seLinuxOptions.type or podSpec.securityContext.seLinuxOptions.type must be set to one of "+
				"\"\"/container_t/container_init_t/container_kvm_t/container_engine_t", errPrefix))
		}
		if se.User != "" {
			errs = append(errs, fmt.Errorf("%s.securityContext.seLinuxOptions.user or podSpec.securityContext.seLinuxOptions.user must be unset", errPrefix))
		}
		if se.Role != "" {
			errs = append(errs, fmt.Errorf("%s.securityContext.seLinuxOptions.role or podSpec.securityContext.seLinuxOptions.role must be unset", errPrefix))
		}
	}
	return errs
}

func mergeSecurityContexts(psc *corev1.PodSecurityContext, sc *corev1.SecurityContext) *corev1.SecurityContext {
	if sc == nil {
		sc = &corev1.SecurityContext{}
	}
	if psc == nil {
		return sc
	}
	if sc.RunAsUser == nil {
		sc.RunAsUser = psc.RunAsUser
	}
	if sc.RunAsNonRoot == nil {
		sc.RunAsNonRoot = psc.RunAsNonRoot
	}
	if sc.SeccompProfile == nil {
		sc.SeccompProfile = psc.SeccompProfile
	}
	if sc.AppArmorProfile == nil {
		sc.AppArmorProfile = psc.AppArmorProfile
	}
	if sc.SELinuxOptions == nil {
		sc.SELinuxOptions = psc.SELinuxOptions
	}
	return sc
}

func validateVolumes(volumes []corev1.Volume) (errs []error) {
	var badVolumeNames []string
	for _, vol := range volumes {
		switch {
		case vol.ConfigMap != nil:
		case vol.Secret != nil:
		case vol.PersistentVolumeClaim != nil:
		case vol.EmptyDir != nil:
			// Assume limits are enforced in backend.
		case vol.Projected != nil:
			for _, source := range vol.Projected.Sources {
				switch {
				case source.ConfigMap != nil:
				case source.Secret != nil:
				default:
					badVolumeNames = append(badVolumeNames, vol.Name)
				}
			}
		default:
			badVolumeNames = append(badVolumeNames, vol.Name)
		}
	}
	if len(badVolumeNames) != 0 {
		errs = append(errs, errBadVolumeNames(badVolumeNames))
	}
	return errs
}

type errBadVolumeNames []string

var allowedVolumeTypes = []string{
	"emptyDir",
	"configMap", "projected.sources[*].configMap",
	"secret", "projected.sources[*].secret",
	"persistentVolumeClaim",
}

func (e errBadVolumeNames) Error() string {
	return fmt.Sprintf("volumes not of allowed types %q: %q", allowedVolumeTypes, []string(e))
}

func validatePorts(ports []corev1.ContainerPort) (errs []error) {
	var hostPortNames []string
	for _, port := range ports {
		name := port.Name
		if name == "" {
			name = fmt.Sprint(port.HostPort)
		}
		if port.HostPort != 0 {
			hostPortNames = append(hostPortNames, name)
		}
	}
	if len(hostPortNames) != 0 {
		errs = append(errs, fmt.Errorf("host ports not allowed: %q", hostPortNames))
	}
	return errs
}

func validateServicePortMatch(
	svc *corev1.Service,
	targetSVCPortNumber int32,
) error {
	for _, sp := range svc.Spec.Ports {
		if sp.Port == targetSVCPortNumber {
			return nil
		}
	}
	return fmt.Errorf("helm chart service %q has no port %d", svc.Name, targetSVCPortNumber)
}

// validateServicePort returns true if one of podSpec's containers matches targetSVCPortNumber
// If no match is found, it returns a reason for no match and false.
// Some other podSpec in the chart may match, so the returned parameters should only be used
// if no other podSpec matches.
func validateServicePort(
	svc *corev1.Service,
	targetSVCPortNumber int32,
	podSpec corev1.PodSpec,
) bool {
	if svc == nil {
		return false
	}

	for _, sp := range svc.Spec.Ports {
		if sp.Port != targetSVCPortNumber {
			continue
		}
		var targetPortID intstr.IntOrString
		if sp.TargetPort.StrVal == "" && sp.TargetPort.IntVal == 0 {
			targetPortID = intstr.FromInt32(sp.Port)
		} else {
			targetPortID = sp.TargetPort
		}
		for _, container := range podSpec.Containers {
			for _, cp := range container.Ports {
				if targetPortID.IntVal != cp.ContainerPort && targetPortID.StrVal != cp.Name {
					continue
				}
				return true
			}
		}
	}

	return false
}

func newBoolPtr(v bool) *bool { return &v }

var randReader = rand.Reader

func generateRandomLabelKV(suffix string) (string, string, error) {
	s, err := generateRandomStr()
	if err != nil {
		return "", "", err
	}
	return "autogen-label-" + suffix, fmt.Sprintf("x%sx", s), nil
}

func generateRandomStr() (string, error) {
	const max = 1_000_000_000
	// crypto/rand must be used to avoid concurrent executions using the same seed.
	n, err := rand.Int(randReader, big.NewInt(max))
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

// configForLogging returns a string representation of the config without API keys
func configForLogging(cfg Config) string {
	rcfg := cfg
	rcfg.HelmRegistryAuthConfig = common.RegistryAuthConfig{}
	rcfg.ImageRegistryAuthConfig = common.RegistryAuthConfig{}
	if len(rcfg.Values) != 0 {
		rcfg.Values = json.RawMessage(`{***}`)
	} else {
		rcfg.Values = json.RawMessage(`{}`)
	}
	// Leave only a few characters from the NGCAPIKey in the rendered config
	if rcfg.NGCAPIKey != "" {
		key := rcfg.NGCAPIKey
		if len(key) > 8 {
			rcfg.NGCAPIKey = key[:4] + "..." + key[len(key)-4:]
		}
	}

	return fmt.Sprintf("%#v", rcfg)
}
