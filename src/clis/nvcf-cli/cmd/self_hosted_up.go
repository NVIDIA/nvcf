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

package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	corev1 "k8s.io/api/core/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/auth"
	"nvcf-cli/internal/selfhosted/installlock"
	"nvcf-cli/internal/selfhosted/kubectx"
	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/state"
)

var (
	upClusterName string
	upNCAID       string
	upRegion      string
	upPlanOnly    bool
)

// Phase numbers per SRD/SDD §6.4. Phases 3, 5 and 8 are synthetic — they emit
// started+completed back-to-back because the underlying work is fused into
// helmfile apply (phase 4 / 7) or hasn't been split out yet.
const (
	phaseControlPlaneNamespacesEnv = "NVCF_CLI_CP_NAMESPACES"
	phaseComputePlaneNamespacesEnv = "NVCF_CLI_COMPUTE_NAMESPACES"
	localPullSecretName            = "nvcr-pull-secret"
	namespaceNVCAOperator          = "nvca-operator"
	namespaceNVCASystem            = "nvca-system"

	upPhasePreflight         = string(selfhosted.PhasePreflight)
	upPhaseResolve           = string(selfhosted.PhaseResolve)
	upPhaseRender            = string(selfhosted.PhaseRender)
	upPhaseApplyCP           = string(selfhosted.PhaseApplyCP)
	upPhaseCheckCP           = string(selfhosted.PhaseCheckCP)
	upPhaseRegister          = string(selfhosted.PhaseRegister)
	upPhaseApplyComputePlane = string(selfhosted.PhaseApplyCompute)
	upPhaseFinalHealth       = string(selfhosted.PhaseFinalCheck)
)

// defaultControlPlaneNamespaces is the watcher's filter for phase 4 (apply-cp).
// Best-effort enumeration of the namespaces that helmfile creates; mismatches
// just degrade the sub-progress to "0/0" without breaking the install.
var defaultControlPlaneNamespaces = []string{
	"sis", "nvcf", "cassandra-system", "openbao-system",
	"api-keys", "ess", "nats-system", "nvcf-backend",
}

// defaultComputePlaneNamespaces is the watcher's filter for phase 7
// (apply-compute-plane). The compute plane lives in nvca-system + nvca-operator
// per the multi-cluster topology.
var defaultComputePlaneNamespaces = []string{
	namespaceNVCASystem, namespaceNVCAOperator,
}

var localPullSecretNamespaces = []string{
	"cassandra-system", "nats-system", "nvcf", "api-keys", "ess", "sis",
	"vault-system", namespaceNVCAOperator, namespaceNVCASystem, "nvcf-backend",
}

const (
	finalHealthTimeout = 5 * time.Minute
	finalHealthPoll    = 5 * time.Second

	namespaceTerminationPoll    = 250 * time.Millisecond
	namespaceTerminationGrace   = 10 * time.Second
	namespaceTerminationTimeout = 2 * time.Minute
)

type computePlaneHealthRequest struct {
	ClusterName string
	KubeContext string
	Timeout     time.Duration
}

type computePlaneHealthResult struct {
	BackendHealth string
}

var waitForComputePlaneHealth = waitForComputePlaneHealthDefault
var selfHostedUpCurrentKubeContext = currentKubeContextName

var selfHostedUpCmd = &cobra.Command{
	Use:          "up",
	Short:        "One-shot install: pre-flight + control plane + register + compute plane",
	SilenceUsage: true,
	RunE:         runSelfHostedUp,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedUpCmd)
	selfHostedUpCmd.Flags().StringVar(&upClusterName, "cluster-name", "", "Cluster name (required)")
	_ = selfHostedUpCmd.MarkFlagRequired("cluster-name")
	selfHostedUpCmd.Flags().StringVar(&upNCAID, "nca-id", "nvcf-default", "NCA ID (account) the cluster registers under")
	selfHostedUpCmd.Flags().StringVar(&upRegion, "region", "us-west-1", "Cluster region (ICMS requires non-empty)")
	selfHostedUpCmd.Flags().BoolVar(&upPlanOnly, "plan-only", false, "Dry-run: emit planned phase sequence + ETAs without changing cluster state")
}

func runSelfHostedUp(c *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := time.Now().UTC()

	// Construct EventSink + selected renderer from --json/--plain/--accessible
	// + ambient state (NO_COLOR / TERM / CI / TTY size).
	// Cluster/Target/Stack populate the bubbletea Model header for TTY modes;
	// they are ignored by the plain, JSONL, and accessible renderers.
	// selfHostedStack is the flag value (before resolution); resolved.Path is
	// not yet available here, so the flag string is the best we have.
	sink, kind, err := progress.SelectRenderer(c.ErrOrStderr(), progress.RenderOpts{
		JSON:                selfHostedJSON,
		Plain:               selfHostedPlain,
		Accessible:          selfHostedAccessible,
		Cluster:             upClusterName,
		Target:              upRegion,
		Stack:               selfHostedStack,
		ControlPlaneContext: selfHostedControlPlaneContext, // M+9.E: split-cluster header
		ComputePlaneContext: selfHostedComputePlaneContext, // M+9.E: split-cluster header
	})
	if err != nil {
		return err
	}
	defer func() { _ = sink.Close() }()

	helmfileStdout, helmfileStderr, restoreOutput := configureUpOutput(c, sink, kind)
	defer restoreOutput()
	startEventSink(sink)

	r := &selfHostedUpRun{
		c:              c,
		ctx:            ctx,
		sink:           sink,
		started:        started,
		helmfileStdout: helmfileStdout,
		helmfileStderr: helmfileStderr,
	}
	return r.run()
}

type selfHostedUpRun struct {
	c               *cobra.Command
	ctx             context.Context
	sink            progress.EventSink
	started         time.Time
	helmfileStdout  io.Writer
	helmfileStderr  io.Writer
	helmRuntimeMode selfhosted.HelmRuntimeMode
}

type upClusterRegistration struct {
	ClusterID      string
	ClusterGroupID string
	NCAID          string
	Region         string
	IdentitySource string
}

func configureUpOutput(c *cobra.Command, sink progress.EventSink, kind progress.RendererKind) (io.Writer, io.Writer, func()) {
	helmfileStdout := c.OutOrStdout()
	helmfileStderr := c.ErrOrStderr()
	if !isUpTTYRenderer(kind) {
		return helmfileStdout, helmfileStderr, noopUpCleanup
	}

	prev := log.Writer()
	log.SetOutput(io.Discard)
	stdoutW := progress.NewLogLineWriter(sink, "stdout", "helmfile")
	stderrW := progress.NewLogLineWriter(sink, "stderr", "helmfile")
	restore := func() {
		_ = stdoutW.Close()
		_ = stderrW.Close()
		log.SetOutput(prev)
	}
	return stdoutW, stderrW, restore
}

func isUpTTYRenderer(kind progress.RendererKind) bool {
	return kind == progress.RendererTTYFull || kind == progress.RendererTTYCompact
}

func startEventSink(sink progress.EventSink) {
	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}
}

func noopUpCleanup() {
	// Nothing was acquired or wrapped, so there is nothing to release.
}

func (r *selfHostedUpRun) run() error {
	if err := validateSelfHostedUpLocalK3DMode(); err != nil {
		return err
	}
	applyLocalEndpointDefaults(resolveICMSURL(selfHostedICMSURL))
	releaseLock, err := r.runPreflightAndLock()
	if err != nil {
		return err
	}
	defer releaseLock()

	resolved, err := r.resolveStack()
	if err != nil {
		return err
	}
	if upPlanOnly {
		return r.emitPlanOnly(resolved.Path)
	}

	r.emitSyntheticPhase(3, upPhaseRender, kubectxFor(3))
	if err := r.applyControlPlane(resolved.Path); err != nil {
		return err
	}
	if err := r.writeControlPlaneProfile(resolved.Path); err != nil {
		return err
	}
	if err := r.checkControlPlane(); err != nil {
		return err
	}
	registration, err := r.registerCluster(resolved.Path)
	if err != nil {
		return err
	}
	if err := r.applyComputePlane(resolved.Path, registration); err != nil {
		return err
	}
	return r.emitFinalHealth(registration)
}

func validateSelfHostedUpLocalK3DMode() error {
	if !strings.EqualFold(selfHostedEnv, "local") {
		return &ExitCodeError{
			Code: 3,
			Msg:  "self-hosted up only supports --env local; use control-plane profile, compute-plane register, and compute-plane install for non-local deployments",
		}
	}
	if kubectx.SelectMode(selfHostedControlPlaneContext, selfHostedComputePlaneContext) != kubectx.ModeSingle {
		return &ExitCodeError{
			Code: 3,
			Msg:  "self-hosted up only supports local k3d single-cluster deployments; use control-plane profile, compute-plane register, and compute-plane install for split-cluster deployments",
		}
	}
	currentContext, err := selfHostedUpCurrentKubeContext()
	if err != nil {
		return fmt.Errorf("read current kube context: %w", err)
	}
	if !strings.HasPrefix(currentContext, "k3d-") {
		return &ExitCodeError{
			Code: 3,
			Msg:  fmt.Sprintf("self-hosted up requires a k3d kube context; current context %q is not k3d. Use control-plane profile, compute-plane register, and compute-plane install for non-local deployments", currentContext),
		}
	}
	return nil
}

func currentKubeContextName() (string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := rules.Load()
	if err != nil {
		return "", err
	}
	return cfg.CurrentContext, nil
}

func (r *selfHostedUpRun) runPreflightAndLock() (func(), error) {
	p1Start, err := r.runPreflight()
	if err != nil {
		return noopUpCleanup, err
	}
	return r.acquireInstallLock(p1Start)
}

func (r *selfHostedUpRun) runPreflight() (time.Time, error) {
	if r.ctx.Err() != nil {
		return time.Time{}, r.emitCancellation(1, upPhasePreflight)
	}
	p1Start := r.emitPhase(1, upPhasePreflight)
	results := runUpPreflight(r.ctx, selfhosted.PreflightConfig{Tools: selfHostedPreflightTools()})
	r.helmRuntimeMode = helmRuntimeModeFromPreflightResults(results)
	selfhosted.RenderText(r.c.ErrOrStderr(), results)
	if anyFailed(results) {
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhasePreflight, Err: fmt.Errorf("pre-flight checks failed")}, p1Start)
		return p1Start, &ExitCodeError{Code: 2, Msg: "pre-flight checks failed"}
	}
	r.emitPhaseDone(1, upPhasePreflight, p1Start)
	if r.ctx.Err() != nil {
		return p1Start, r.emitCancellation(2, upPhaseResolve)
	}
	return p1Start, nil
}

func (r *selfHostedUpRun) acquireInstallLock(p1Start time.Time) (func(), error) {
	kubeForLock, _, kerr := buildKubeClientsForWatcher()
	if kerr != nil {
		r.emitInstallLockWarning("install lock unavailable (kubeconfig: " + kerr.Error() + ")")
		return noopUpCleanup, nil
	}

	lk := installlock.NewLock(kubeForLock, lockKey(upNCAID, upClusterName), installlock.Options{})
	if lerr := lk.Acquire(r.ctx); lerr != nil {
		return r.handleInstallLockError(lerr, p1Start)
	}
	return func() { _ = lk.Release(context.Background()) }, nil
}

func (r *selfHostedUpRun) handleInstallLockError(lerr error, p1Start time.Time) (func(), error) {
	if errors.Is(lerr, installlock.ErrAlreadyHeld) {
		r.emitFailure(selfhosted.Failure{
			Phase: selfhosted.PhasePreflight,
			Err:   fmt.Errorf("another nvcf-cli self-hosted up is in progress: %w", lerr),
		}, p1Start)
		return noopUpCleanup, &ExitCodeError{Code: 1, Msg: lerr.Error()}
	}
	r.emitInstallLockWarning("install lock unavailable (proceeding without): " + lerr.Error())
	return noopUpCleanup, nil
}

func (r *selfHostedUpRun) emitInstallLockWarning(detail string) {
	_ = r.sink.Emit(r.ctx, progress.LastProgress{
		Num:    1,
		Detail: detail,
		At:     time.Now().UTC(),
	})
}

func (r *selfHostedUpRun) resolveStack() (*selfhosted.ResolvedStack, error) {
	p2Start := r.emitPhase(2, upPhaseResolve)
	resolved, err := selfhosted.ResolveStack(r.ctx, selfhosted.StackOptions{
		Source:        selfHostedStack,
		BuiltInOCIRef: builtInStackOCI(),
	})
	if err != nil {
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseResolve, Err: err}, p2Start)
		return nil, err
	}
	r.emitPhaseDone(2, upPhaseResolve, p2Start)
	if r.ctx.Err() != nil {
		return nil, r.emitCancellation(3, upPhaseRender)
	}
	return resolved, nil
}

func (r *selfHostedUpRun) emitPlanOnly(stackPath string) error {
	phases := progress.AllPhaseETAs()
	var totalETA int
	for _, p := range phases {
		totalETA += p.ETASec
	}
	_ = r.sink.Emit(r.ctx, progress.Planned{
		Cluster:     upClusterName,
		Target:      upRegion,
		Stack:       stackPath,
		Phases:      phases,
		TotalETASec: totalETA,
	})
	_ = r.sink.Emit(r.ctx, progress.Final{
		PlanOnly: true,
		Success:  true,
		Duration: time.Since(r.started),
	})
	return nil
}

func (r *selfHostedUpRun) applyControlPlane(stackPath string) error {
	p4Start := r.emitPhaseCtx(4, upPhaseApplyCP, kubectxFor(4))
	if err := r.ensureLocalImagePullSecrets(kubectxFor(4), 4); err != nil {
		return r.handleHelmfilePhaseError(4, upPhaseApplyCP, selfhosted.PhaseApplyCP, "prepare image pull secrets", err, p4Start)
	}
	cancelWatcher, watcherDone := startWatcher(r.ctx, r.sink, 4, upPhaseApplyCP, controlPlaneNamespaces())
	err := selfhosted.Render(selfhosted.RenderOptions{
		StackPath:       stackPath,
		Env:             selfHostedEnv,
		Apply:           true,
		HelmRuntimeMode: r.helmRuntimeMode,
		KubeContext:     kubectxFor(4), // M+9.E: control-plane context in split mode
		Stdout:          r.helmfileStdout,
		Stderr:          r.helmfileStderr,
		Ctx:             r.ctx,
	})
	stopWatcher(cancelWatcher, watcherDone)
	if err != nil {
		return r.handleHelmfilePhaseError(4, upPhaseApplyCP, selfhosted.PhaseApplyCP, "control-plane install", err, p4Start)
	}
	r.emitPhaseDoneCtx(4, upPhaseApplyCP, kubectxFor(4), p4Start)
	if r.ctx.Err() != nil {
		return r.emitCancellation(5, upPhaseCheckCP)
	}
	return nil
}

func (r *selfHostedUpRun) writeControlPlaneProfile(stackPath string) error {
	path, err := writeControlPlaneProfile(controlPlaneProfileWriteRequest{
		StackPath:           stackPath,
		ClusterName:         upClusterName,
		NCAID:               upNCAID,
		Region:              upRegion,
		Env:                 selfHostedEnv,
		ControlPlaneContext: selfHostedControlPlaneContext,
		ComputePlaneContext: selfHostedComputePlaneContext,
		ICMSURL:             resolveICMSURL(selfHostedICMSURL),
		NATSURL:             selfHostedNATSURL,
	})
	if err != nil {
		wrapped := fmt.Errorf("writing control-plane profile: %w", err)
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseApplyCP, Err: wrapped}, time.Now().UTC())
		return wrapped
	}
	_ = r.sink.Emit(r.ctx, progress.LastProgress{
		Num:     4,
		Detail:  "Wrote control-plane profile: " + path,
		At:      time.Now().UTC(),
		Context: kubectxFor(4),
	})
	return nil
}

func (r *selfHostedUpRun) checkControlPlane() error {
	p5Start := r.emitPhaseCtx(5, upPhaseCheckCP, kubectxFor(5))
	if selfHostedToken == "" {
		if err := authGatePhase5(r.ctx, r.sink, p5Start); err != nil {
			r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseCheckCP, Err: err}, p5Start)
			return fmt.Errorf("auth-gate: %w", err)
		}
	}
	r.emitPhaseDoneCtx(5, upPhaseCheckCP, kubectxFor(5), p5Start)
	if r.ctx.Err() != nil {
		return r.emitCancellation(6, upPhaseRegister)
	}
	return nil
}

func (r *selfHostedUpRun) registerCluster(stackPath string) (upClusterRegistration, error) {
	p6Start := r.emitPhaseCtx(6, upPhaseRegister, kubectxFor(6))
	registration, err := r.createClusterRegistration(stackPath, p6Start)
	if err != nil {
		return upClusterRegistration{}, err
	}
	r.emitPhaseDoneCtx(6, upPhaseRegister, kubectxFor(6), p6Start)
	_ = r.sink.Emit(r.ctx, progress.LastProgress{
		Num:    6,
		Detail: "cluster registered: " + registration.ClusterID,
		At:     time.Now().UTC(),
	})
	if r.ctx.Err() != nil {
		return upClusterRegistration{}, r.emitCancellation(7, upPhaseApplyComputePlane)
	}
	return registration, nil
}

func (r *selfHostedUpRun) createClusterRegistration(stackPath string, p6Start time.Time) (upClusterRegistration, error) {
	icmsURL := resolveICMSURL(selfHostedICMSURL)
	cc, err := newClusterClientForSelfHosted(icmsURL)
	if err != nil {
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: err}, p6Start)
		return upClusterRegistration{}, err
	}
	defer cc.Close()

	oidcIssuer, jwks, identitySource, err := fetchClusterIdentity(r.ctx, kubectxFor(6))
	if err != nil {
		wrapped := fmt.Errorf("fetching cluster JWKS: %w", err)
		r.emitFailure(selfhosted.Failure{
			Phase:            selfhosted.PhaseRegister,
			Err:              wrapped,
			KubernetesReason: "JWKSFetchFailed",
		}, p6Start)
		return upClusterRegistration{}, wrapped
	}
	registration := upClusterRegistration{
		NCAID:          getenvDefault("NCA_ID", upNCAID),
		Region:         getenvDefault("CLUSTER_REGION", upRegion),
		IdentitySource: defaultString(identitySource, "psat"),
	}
	if selfHostedEnv == "local" {
		deleted := 0
		if rv, err := readRegisterValuesYAML(stackPath, upClusterName); err == nil && rv.ClusterID != "" {
			if err := cc.DeleteCluster(r.ctx, rv.ClusterID); err != nil {
				wrapped := fmt.Errorf("delete stale cluster registration %s: %w", rv.ClusterID, err)
				r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: wrapped, HTTPStatus: httpStatusFromErr(err)}, p6Start)
				return upClusterRegistration{}, wrapped
			}
			deleted++
		} else if err != nil && !os.IsNotExist(err) {
			wrapped := fmt.Errorf("read stale register-values: %w", err)
			r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: wrapped}, p6Start)
			return upClusterRegistration{}, wrapped
		}
		deletedByName, err := cc.DeleteClusterByName(r.ctx, registration.NCAID, upClusterName)
		if err != nil {
			wrapped := fmt.Errorf("delete existing cluster registration: %w", err)
			r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: wrapped, HTTPStatus: httpStatusFromErr(err)}, p6Start)
			return upClusterRegistration{}, wrapped
		}
		deleted += deletedByName
		if deleted > 0 {
			_ = r.sink.Emit(r.ctx, progress.LastProgress{
				Num:    6,
				Detail: fmt.Sprintf("removed %d existing cluster registration(s)", deleted),
				At:     time.Now().UTC(),
			})
		}
	}
	resp, err := cc.RegisterCluster(r.ctx, selfhosted.RegisterRequest{
		ClusterName:    upClusterName,
		NCAID:          registration.NCAID,
		Region:         registration.Region,
		JWKS:           jwks,
		OIDCIssuer:     oidcIssuer,
		IdentitySource: registration.IdentitySource,
	})
	if err != nil {
		wrapped := fmt.Errorf("cluster register: %w", err)
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: wrapped, HTTPStatus: httpStatusFromErr(err)}, p6Start)
		return upClusterRegistration{}, wrapped
	}
	registration.ClusterID = resp.ClusterID
	registration.ClusterGroupID = resp.ClusterGroupID
	return registration, r.writeRegistrationValues(stackPath, icmsURL, registration, p6Start)
}

func (r *selfHostedUpRun) writeRegistrationValues(stackPath, icmsURL string, registration upClusterRegistration, p6Start time.Time) error {
	endpoints := resolveNVCAEndpointValues(selfHostedEnv, selfHostedControlPlaneContext, selfHostedComputePlaneContext, icmsURL, selfHostedNATSURL)
	if err := writeRegisterValuesYAML(registerValuesWriteRequest{
		StackPath:      stackPath,
		ClusterName:    upClusterName,
		NCAID:          registration.NCAID,
		Region:         registration.Region,
		IdentitySource: registration.IdentitySource,
		ClusterID:      registration.ClusterID,
		ClusterGroupID: registration.ClusterGroupID,
		Endpoints:      endpoints,
	}); err != nil {
		wrapped := fmt.Errorf("writing register-values: %w", err)
		r.emitFailure(selfhosted.Failure{Phase: selfhosted.PhaseRegister, Err: wrapped}, p6Start)
		return wrapped
	}
	_ = r.sink.Emit(r.ctx, progress.LastProgress{
		Num:    6,
		Detail: "Wrote NVCA values: " + nvcaValuesPath(stackPath, upClusterName),
		At:     time.Now().UTC(),
	})
	return nil
}

func (r *selfHostedUpRun) applyComputePlane(stackPath string, registration upClusterRegistration) error {
	p7Start := r.emitPhaseCtx(7, upPhaseApplyComputePlane, kubectxFor(7))
	if err := r.ensureLocalImagePullSecrets(kubectxFor(7), 7); err != nil {
		return r.handleHelmfilePhaseError(7, upPhaseApplyComputePlane, selfhosted.PhaseApplyCompute, "prepare image pull secrets", err, p7Start)
	}
	cancelWatcher, watcherDone := startWatcher(r.ctx, r.sink, 7, upPhaseApplyComputePlane, computePlaneNamespaces())
	helmfileFile, selector := computePlaneTarget(stackPath)
	err := selfhosted.Render(selfhosted.RenderOptions{
		StackPath:       stackPath,
		HelmfileFile:    helmfileFile,
		Env:             selfHostedEnv,
		Apply:           true,
		Selector:        selector,
		HelmRuntimeMode: r.helmRuntimeMode,
		KubeContext:     kubectxFor(7), // M+9.E: compute-plane context in split mode
		Stdout:          r.helmfileStdout,
		Stderr:          r.helmfileStderr,
		Ctx:             r.ctx,
		ExtraEnv:        append(registration.computePlaneEnv(), "NVCF_NVCA_VALUES_FILE="+nvcaValuesPath(stackPath, upClusterName)),
	})
	stopWatcher(cancelWatcher, watcherDone)
	if err != nil {
		return r.handleHelmfilePhaseError(7, upPhaseApplyComputePlane, selfhosted.PhaseApplyCompute, "compute-plane install", err, p7Start)
	}
	r.emitPhaseDoneCtx(7, upPhaseApplyComputePlane, kubectxFor(7), p7Start)
	if r.ctx.Err() != nil {
		return r.emitCancellation(8, upPhaseFinalHealth)
	}
	return nil
}

func (r *selfHostedUpRun) emitFinalHealth(registration upClusterRegistration) error {
	p8Start := r.emitPhaseCtx(8, upPhaseFinalHealth, kubectxFor(8))
	health, err := waitForComputePlaneHealth(r.ctx, computePlaneHealthRequest{
		ClusterName: upClusterName,
		KubeContext: kubectxFor(8),
		Timeout:     finalHealthTimeout,
	})
	if err != nil {
		r.emitFailure(selfhosted.Failure{
			Phase:            selfhosted.PhaseFinalCheck,
			Err:              err,
			KubernetesReason: "ComputePlaneNotReady",
		}, p8Start)
		return &ExitCodeError{Code: 2, Msg: "final health check failed: " + err.Error()}
	}
	r.emitPhaseDoneCtx(8, upPhaseFinalHealth, kubectxFor(8), p8Start)
	_ = r.sink.Emit(r.ctx, progress.Final{
		Success:           true,
		ClusterID:         registration.ClusterID,
		ClusterGroupID:    registration.ClusterGroupID,
		NVCFBackendHealth: health.BackendHealth,
		Duration:          time.Since(r.started),
	})
	return nil
}

func (r *selfHostedUpRun) ensureLocalImagePullSecrets(kubeContext string, phaseNum int) error {
	if selfHostedEnv != "local" {
		return nil
	}
	apiKey := firstNonEmptyEnv("NGC_IMAGE_PULL_API_KEY", "NVCF_NGCR_API_KEY", "NVCF_NGC_API_KEY", "NGC_API_KEY")
	if apiKey == "" {
		_ = r.sink.Emit(r.ctx, progress.LastProgress{
			Num:     phaseNum,
			Detail:  "NGC_API_KEY not set; expecting pre-created nvcr-pull-secret in local namespaces",
			At:      time.Now().UTC(),
			Context: kubeContext,
		})
		return nil
	}
	kube, err := buildKubeClientForCtx(kubeContext)
	if err != nil {
		return fmt.Errorf("build kube client for pull secrets: %w", err)
	}
	dockerConfig, err := dockerConfigJSON("nvcr.io", "$oauthtoken", apiKey)
	if err != nil {
		return err
	}
	for _, ns := range localPullSecretNamespaces {
		if err := ensureNamespace(r.ctx, kube, ns); err != nil {
			return err
		}
		if err := ensureDockerConfigSecret(r.ctx, kube, ns, localPullSecretName, dockerConfig); err != nil {
			return err
		}
	}
	_ = r.sink.Emit(r.ctx, progress.LastProgress{
		Num:     phaseNum,
		Detail:  fmt.Sprintf("ensured %s in %d local namespaces", localPullSecretName, len(localPullSecretNamespaces)),
		At:      time.Now().UTC(),
		Context: kubeContext,
	})
	return nil
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func dockerConfigJSON(registry, username, password string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return json.Marshal(map[string]any{
		"auths": map[string]any{
			registry: map[string]string{
				"username": username,
				"password": password,
				"auth":     auth,
			},
		},
	})
}

func ensureNamespace(ctx context.Context, kube kubernetes.Interface, name string) error {
	for {
		ns, err := kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			if namespaceIsTerminating(ns) {
				if err := waitForNamespaceDeletion(ctx, kube, name); err != nil {
					return err
				}
				continue
			}
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get namespace %s: %w", name, err)
		}
		_, err = kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create namespace %s: %w", name, err)
		}
		return nil
	}
}

func namespaceIsTerminating(ns *corev1.Namespace) bool {
	return ns.Status.Phase == corev1.NamespaceTerminating || ns.DeletionTimestamp != nil
}

func waitForNamespaceDeletion(ctx context.Context, kube kubernetes.Interface, name string) error {
	started := time.Now()
	ticker := time.NewTicker(namespaceTerminationPoll)
	defer ticker.Stop()
	cleanedFinalizers := false
	for {
		ns, err := kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("get terminating namespace %s: %w", name, err)
		case !namespaceIsTerminating(ns):
			return nil
		}
		elapsed := time.Since(started)
		if !cleanedFinalizers && elapsed >= namespaceTerminationGrace {
			if err := clearNamespaceFinalizers(ctx, kube, ns); err != nil {
				return err
			}
			cleanedFinalizers = true
		}
		if elapsed >= namespaceTerminationTimeout {
			return fmt.Errorf("namespace %s still terminating after %s", name, namespaceTerminationTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func clearNamespaceFinalizers(ctx context.Context, kube kubernetes.Interface, ns *corev1.Namespace) error {
	if len(ns.Spec.Finalizers) == 0 {
		return nil
	}
	cp := ns.DeepCopy()
	cp.Spec.Finalizers = nil
	if _, err := kube.CoreV1().Namespaces().Finalize(ctx, cp, metav1.UpdateOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("clear namespace finalizers %s: %w", ns.Name, err)
	}
	return nil
}

func ensureDockerConfigSecret(ctx context.Context, kube kubernetes.Interface, namespace, name string, dockerConfig []byte) error {
	secrets := kube.CoreV1().Secrets(namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfig},
	}
	current, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		current.Type = corev1.SecretTypeDockerConfigJson
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		current.Data[corev1.DockerConfigJsonKey] = dockerConfig
		if _, err := secrets.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update pull secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get pull secret %s/%s: %w", namespace, name, err)
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pull secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func waitForComputePlaneHealthDefault(ctx context.Context, req computePlaneHealthRequest) (computePlaneHealthResult, error) {
	if req.ClusterName == "" {
		return computePlaneHealthResult{}, fmt.Errorf("cluster name is required")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = finalHealthTimeout
	}
	kube, err := buildKubeClientForCtx(req.KubeContext)
	if err != nil {
		return computePlaneHealthResult{}, fmt.Errorf("build kube client: %w", err)
	}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		backendHealth, backendErr := getNVCFBackendHealth(ctx, req.KubeContext, req.ClusterName)
		operatorReady, operatorReason, operatorErr := namespacePodsReady(ctx, kube, namespaceNVCAOperator)
		systemReady, systemReason, systemErr := namespacePodsReady(ctx, kube, namespaceNVCASystem)
		switch {
		case backendErr != nil:
			last = backendErr.Error()
		case !strings.EqualFold(backendHealth, "healthy"):
			last = "NVCFBackend health is " + defaultString(backendHealth, "unknown")
		case operatorErr != nil:
			last = operatorErr.Error()
		case !operatorReady:
			last = namespaceNVCAOperator + " not ready: " + operatorReason
		case systemErr != nil:
			last = systemErr.Error()
		case !systemReady:
			last = namespaceNVCASystem + " not ready: " + systemReason
		default:
			return computePlaneHealthResult{BackendHealth: backendHealth}, nil
		}
		if time.Now().After(deadline) {
			return computePlaneHealthResult{}, fmt.Errorf("timed out waiting for compute plane: %s", last)
		}
		select {
		case <-ctx.Done():
			return computePlaneHealthResult{}, ctx.Err()
		case <-time.After(finalHealthPoll):
		}
	}
}

func getNVCFBackendHealth(ctx context.Context, kubeContext, clusterName string) (string, error) {
	for _, field := range []string{".status.agentStatus", ".status.health"} {
		health, err := getNVCFBackendHealthField(ctx, kubeContext, clusterName, field)
		if err != nil {
			return "", err
		}
		if health != "" {
			return health, nil
		}
	}
	return "", nil
}

func getNVCFBackendHealthField(ctx context.Context, kubeContext, clusterName, field string) (string, error) {
	args := []string{}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	args = append(args, "get", "nvcfbackend", "-n", namespaceNVCAOperator, clusterName, "-o", "jsonpath={"+field+"}")
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func namespacePodsReady(ctx context.Context, kube kubernetes.Interface, namespace string) (bool, string, error) {
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, "", fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	active := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		active++
		if pod.Status.Phase != corev1.PodRunning {
			return false, pod.Name + " phase=" + string(pod.Status.Phase), nil
		}
		if !podReady(pod) {
			return false, pod.Name + " containers not ready", nil
		}
	}
	if active == 0 {
		return false, "no active pods found", nil
	}
	return true, "", nil
}

func podReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (r *selfHostedUpRun) handleHelmfilePhaseError(num int, name string, phase selfhosted.PhaseID, prefix string, err error, started time.Time) error {
	if errors.Is(err, context.Canceled) || r.ctx.Err() != nil {
		return r.emitCancellation(num, name)
	}
	r.emitFailure(selfhosted.Failure{
		Phase:      phase,
		Err:        err,
		Subprocess: "helmfile",
		ExitCode:   exitCodeFromErr(err),
	}, started)
	return fmt.Errorf("%s: %w", prefix, err)
}

func (r *selfHostedUpRun) emitCancellation(phaseNum int, phaseName string) error {
	_ = r.sink.Emit(context.Background(), progress.PhaseCancelled{
		Num:    phaseNum,
		Name:   phaseName,
		Reason: "sigint",
	})
	_ = r.sink.Emit(context.Background(), progress.Final{
		Cancelled: true,
		Duration:  time.Since(r.started),
	})
	return &ExitCodeError{Code: 130, Msg: "interrupted"}
}

func (r *selfHostedUpRun) emitPhase(num int, name string) time.Time {
	t := time.Now().UTC()
	_ = r.sink.Emit(r.ctx, progress.PhaseStarted{Num: num, Name: name, StartedAt: t})
	return t
}

func (r *selfHostedUpRun) emitPhaseDone(num int, name string, start time.Time) {
	_ = r.sink.Emit(r.ctx, progress.PhaseCompleted{
		Num:      num,
		Name:     name,
		Duration: time.Since(start),
	})
}

func (r *selfHostedUpRun) emitPhaseCtx(num int, name, kctx string) time.Time {
	t := time.Now().UTC()
	_ = r.sink.Emit(r.ctx, progress.PhaseStarted{Num: num, Name: name, StartedAt: t, Context: kctx})
	return t
}

func (r *selfHostedUpRun) emitPhaseDoneCtx(num int, name, kctx string, start time.Time) {
	_ = r.sink.Emit(r.ctx, progress.PhaseCompleted{
		Num:      num,
		Name:     name,
		Duration: time.Since(start),
		Context:  kctx,
	})
}

func (r *selfHostedUpRun) emitSyntheticPhase(num int, name, kctx string) {
	started := r.emitPhaseCtx(num, name, kctx)
	r.emitPhaseDoneCtx(num, name, kctx, started)
}

func (r *selfHostedUpRun) emitFailure(f selfhosted.Failure, started time.Time) {
	_ = emitStructuredFailure(r.ctx, r.sink, f, started)
	r.emitFinalFailure()
}

func (r *selfHostedUpRun) emitFinalFailure() {
	_ = r.sink.Emit(r.ctx, progress.Final{
		Success:  false,
		Duration: time.Since(r.started),
	})
}

func (r upClusterRegistration) computePlaneEnv() []string {
	return []string{
		"CLUSTER_NAME=" + upClusterName,
		"CLUSTER_ID=" + r.ClusterID,
		"CLUSTER_GROUP_ID=" + r.ClusterGroupID,
		"IDENTITY_SOURCE=" + r.IdentitySource,
		"NCA_ID=" + r.NCAID,
		"CLUSTER_REGION=" + r.Region,
	}
}

func getenvDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// emitStructuredFailure categorizes a Failure (via selfhosted.Categorize) and
// emits a fully-populated PhaseFailed event with duration, category, retry
// class, remediation strings, and Raw sub-fields all filled.
func emitStructuredFailure(
	ctx context.Context,
	sink progress.EventSink,
	f selfhosted.Failure,
	started time.Time,
) error {
	pf := selfhosted.Categorize(f)
	pf.Duration = time.Since(started)
	return sink.Emit(ctx, pf)
}

// exitCodeFromErr extracts a subprocess exit code from a wrapped *exec.ExitError
// if present. Returns 1 as a reasonable non-zero default when the error doesn't
// carry an explicit exit code.
func exitCodeFromErr(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// httpStatusFromErr performs a best-effort extraction of an HTTP status code
// embedded in the error message (e.g. "status 409: ..."). Returns 0 when no
// status is parseable — callers treat 0 as "not an HTTP error".
func httpStatusFromErr(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	// Look for patterns like "status 409", "HTTP 503", "503 service unavailable",
	// or the client package's "unexpected status: 422" convention.
	for _, prefix := range []string{"status ", "HTTP ", "status: "} {
		lower := strings.ToLower(msg)
		idx := strings.Index(lower, strings.ToLower(prefix))
		if idx == -1 {
			continue
		}
		rest := msg[idx+len(prefix):]
		code := parseLeadingInt(rest)
		if code >= 100 && code <= 599 {
			return code
		}
	}
	return 0
}

// parseLeadingInt reads a non-negative integer from the start of s and returns
// it. Returns 0 on any parse failure.
func parseLeadingInt(s string) int {
	val := 0
	found := false
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			val = val*10 + int(ch-'0')
			found = true
		} else {
			break
		}
	}
	if !found {
		return 0
	}
	return val
}

// startWatcher launches WatchResources in a goroutine. Returns a cancel func
// and a done channel; pair them with stopWatcher to join cleanly. Watcher
// errors (kubeconfig unloadable, malformed) are swallowed — the install
// proceeds without sub-progress, which is graceful degradation rather than
// a failure mode.
func startWatcher(
	parent context.Context,
	sink progress.EventSink,
	phaseNum int,
	phaseName string,
	namespaces []string,
) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		kubeClient, apiextClient, err := buildKubeClientsForWatcher()
		if err != nil {
			// Kubeconfig unavailable; renderer just won't see PhaseProgress
			// events for this phase. The install still works.
			return
		}
		_ = progress.WatchResources(ctx, kubeClient, apiextClient, namespaces, phaseNum, phaseName, sink)
	}()
	return cancel, done
}

// stopWatcher cancels the watcher context and waits for the goroutine to drain.
// Idempotent in the cancel sense (CancelFunc is) and required even on the
// success path — the watcher otherwise outlives the function until parent ctx
// is cancelled, which can leak informers in tests.
func stopWatcher(cancel context.CancelFunc, done <-chan struct{}) {
	cancel()
	<-done
}

// buildKubeClientsForWatcher constructs a kubernetes.Interface and an
// apiextensionsclientset.Interface from the user's kubeconfig (KUBECONFIG env
// or ~/.kube/config). Used to power the sub-progress watcher during phases 4
// and 7. Errors are non-fatal at the call site; callers degrade gracefully
// and skip the watcher.
//
// Exposed as a package-level var so tests can swap in a fake client without
// requiring a kubeconfig in CI.
var buildKubeClientsForWatcher = func() (kubernetes.Interface, apiextclientset.Interface, error) {
	cfg, err := loadKubeConfigForWatcher()
	if err != nil {
		return nil, nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	apiextClient, err := apiextclientset.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return kubeClient, apiextClient, nil
}

// loadKubeConfigForWatcher resolves the user's kubeconfig using the standard
// client-go loading rules (--kubeconfig flag absent here, so it falls back to
// KUBECONFIG env then ~/.kube/config).
func loadKubeConfigForWatcher() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// controlPlaneNamespaces returns the namespace filter for the phase-4 watcher.
// NVCF_CLI_CP_NAMESPACES (comma-separated) overrides the default list — useful
// when a custom helmfile lays things out differently.
func controlPlaneNamespaces() []string {
	if v := os.Getenv(phaseControlPlaneNamespacesEnv); v != "" {
		return splitAndTrim(v)
	}
	return defaultControlPlaneNamespaces
}

// computePlaneNamespaces returns the namespace filter for the phase-7 watcher.
// NVCF_CLI_COMPUTE_NAMESPACES overrides the default for parity with the
// control-plane env-var.
func computePlaneNamespaces() []string {
	if v := os.Getenv(phaseComputePlaneNamespacesEnv); v != "" {
		return splitAndTrim(v)
	}
	return defaultComputePlaneNamespaces
}

func splitAndTrim(csv string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			s := csv[start:i]
			// trim whitespace
			for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
				s = s[1:]
			}
			for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
				s = s[:len(s)-1]
			}
			if s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	return out
}

// runUpPreflight is a package-level seam so unit tests can short-circuit the
// pre-flight binary probes (kubectl/helmfile/helm version) without staging a
// fully-functional fake of each tool. Production callers run the real check.
var runUpPreflight = func(ctx context.Context, cfg selfhosted.PreflightConfig) []selfhosted.CheckResult {
	return selfhosted.RunPreflight(ctx, cfg)
}

func helmRuntimeModeFromPreflightResults(results []selfhosted.CheckResult) selfhosted.HelmRuntimeMode {
	for _, r := range results {
		if r.ID == "local-host-tools-helm-runtime" && r.Passed && r.Detail != "" {
			mode := selfhosted.HelmRuntimeMode(r.Detail)
			if selfhosted.IsKnownHelmRuntimeMode(mode) {
				return mode
			}
		}
	}
	return selfhosted.HelmRuntimeHelm3Legacy
}

// runSelfHostedInit shells out to the existing nvcf-cli init command — the
// runtime equivalent of the user typing `nvcf-cli init` themselves. Exposed
// as a package-level var so tests can swap it out without spawning subprocesses.
var runSelfHostedInit = func(ctx context.Context) error {
	c := exec.CommandContext(ctx, os.Args[0], selfHostedInitArgs()...)
	c.Stdin = os.Stdin
	c.Stdout = io.Discard
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func selfHostedInitArgs() []string {
	if cfgFile == "" {
		return []string{"init"}
	}
	return []string{"--config", cfgFile, "init"}
}

// authProbe is a package-level seam so tests can override auth.Probe without
// requiring a real HTTP server. Production callers use auth.Probe directly.
var authProbe = auth.Probe

// stdinIsTerminal is a package-level seam so tests can simulate TTY / non-TTY
// stdin without manipulating the actual file descriptor. Production callers
// resolve to term.IsTerminal against os.Stdin.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// authGatePhase5 implements the M+8.G decision tree:
//  1. Probe control-plane fingerprint from config.BaseHTTPURL.
//  2. Load cached SelfHostedAuth from state.
//  3. If cached.Valid(now, fp) && !--refresh-token → use cached token; skip init.
//  4. Else: run init (which writes Token+TokenExpiration to state).
//     After init returns, re-load state, capture Token+TokenExpiration,
//     and save SelfHostedAuth{Token, ExpiresAt, Fingerprint} → State.Save().
//
// Errors from Probe are non-fatal in "fall-through" mode: cache_corruption
// or network errors → fall through to re-mint via init. Only init errors
// are reported as phase_failed.
func authGatePhase5(ctx context.Context, sink progress.EventSink, _ time.Time) error {
	fp, err := probeControlPlaneFingerprint(ctx, sink)
	if err != nil {
		return err
	}
	if !selfHostedRefreshToken && tryUseCachedAdminToken(ctx, sink, fp) {
		return nil
	}
	// REQ-8: with no cached token usable, the next step would prompt via
	// `nvcf-cli init`. Bail with a clear, actionable error when --non-interactive
	// is set or stdin is not a TTY, rather than letting init block on a stdin read
	// in CI.
	if selfHostedNonInter || !stdinIsTerminal() {
		return fmt.Errorf("admin token required but cannot prompt " +
			"(--non-interactive set or stdin is not a TTY); " +
			"pass --token=$JWT or run `nvcf-cli init` interactively first")
	}
	_ = sink.Emit(ctx, progress.LastProgress{
		Num:    5,
		Detail: "minting admin token via API Keys service",
		At:     time.Now().UTC(),
	})
	if err := runSelfHostedInit(ctx); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	persistAdminAuthAfterInit(ctx, sink, fp)
	return nil
}

// probeControlPlaneFingerprint loads the client config and probes the
// control-plane fingerprint. Probe failures are non-fatal: a nil fingerprint
// signals the caller to fall through to a re-mint via init.
func probeControlPlaneFingerprint(ctx context.Context, sink progress.EventSink) (*auth.Fingerprint, error) {
	cfg, err := client.LoadConfigWithoutAuth()
	if err != nil {
		return nil, fmt.Errorf("load client config: %w", err)
	}
	if cfg.BaseHTTPURL == "" {
		return nil, nil
	}
	fp, probeErr := authProbe(ctx, cfg.BaseHTTPURL)
	if probeErr != nil {
		_ = sink.Emit(ctx, progress.LastProgress{
			Num:    5,
			Detail: fmt.Sprintf("fingerprint probe failed (%v); falling through to re-mint", probeErr),
			At:     time.Now().UTC(),
		})
		return nil, nil
	}
	return fp, nil
}

// tryUseCachedAdminToken returns true and assigns selfHostedToken when a
// valid cached admin token matching the probed fingerprint is found.
func tryUseCachedAdminToken(ctx context.Context, sink progress.EventSink, fp *auth.Fingerprint) bool {
	if fp == nil {
		return false
	}
	sm := state.NewStateManager()
	if err := sm.Load(); err != nil {
		return false
	}
	cached := sm.GetState().SelfHostedAuth
	if cached == nil || cached.Token == "" {
		return false
	}
	cache := &auth.Cache{
		Token:       cached.Token,
		ExpiresAt:   cached.ExpiresAt,
		Fingerprint: fingerprintFromRef(cached.Fingerprint),
	}
	if !cache.Valid(time.Now().UTC(), fp) {
		return false
	}
	_ = sink.Emit(ctx, progress.LastProgress{
		Num:    5,
		Detail: "using cached admin token (fingerprint matches)",
		At:     time.Now().UTC(),
	})
	selfHostedToken = cached.Token
	return true
}

// persistAdminAuthAfterInit re-reads the state file (which init populated),
// then writes the SelfHostedAuth record so a subsequent run can match the
// cached token against the probed fingerprint. Failures here are warnings,
// not errors: init already wrote the token, so the next run can still use
// it; it just won't short-circuit on the fingerprint match.
func persistAdminAuthAfterInit(ctx context.Context, sink progress.EventSink, fp *auth.Fingerprint) {
	if fp == nil {
		return
	}
	sm := state.NewStateManager()
	if err := sm.Load(); err != nil {
		_ = sink.Emit(ctx, progress.LastProgress{
			Num:    5,
			Detail: fmt.Sprintf("warn: load state after init failed (%v); fingerprint not persisted", err),
			At:     time.Now().UTC(),
		})
		return
	}
	s := sm.GetState()
	if s.Token == "" {
		return
	}
	s.SelfHostedAuth = &state.SelfHostedAuth{
		Token:       s.Token,
		ExpiresAt:   s.TokenExpiration,
		Fingerprint: fingerprintRefFromAuth(fp),
	}
	if err := sm.Save(); err != nil {
		_ = sink.Emit(ctx, progress.LastProgress{
			Num:    5,
			Detail: fmt.Sprintf("warn: save SelfHostedAuth failed (%v)", err),
			At:     time.Now().UTC(),
		})
	}
}

// fingerprintFromRef converts the persisted state.FingerprintRef to the
// runtime auth.Fingerprint type.
func fingerprintFromRef(ref *state.FingerprintRef) *auth.Fingerprint {
	if ref == nil {
		return nil
	}
	return &auth.Fingerprint{
		IssuerURL:       ref.IssuerURL,
		JWKSKid:         ref.JWKSKid,
		APIKeysEndpoint: ref.APIKeysEndpoint,
	}
}

// fingerprintRefFromAuth converts the runtime auth.Fingerprint back to
// the persisted state.FingerprintRef.
func fingerprintRefFromAuth(fp *auth.Fingerprint) *state.FingerprintRef {
	if fp == nil {
		return nil
	}
	return &state.FingerprintRef{
		IssuerURL:       fp.IssuerURL,
		JWKSKid:         fp.JWKSKid,
		APIKeysEndpoint: fp.APIKeysEndpoint,
	}
}

// lockKey returns the unique conflict surface for the install lock: NCA ID +
// cluster name identify the specific cluster being installed, which is the
// minimal key that would cause two concurrent `up` invocations to race.
func lockKey(ncaID, clusterName string) string {
	return ncaID + "--" + clusterName
}

// kubectxFor returns the kubeconfig context that phase phaseNum's kubectl /
// helmfile invocations should target. Returns "" in single-cluster mode (both
// context flags empty) so callers pass "" to Render/fetchClusterIdentity and
// the existing behaviour is preserved.
//
// SRD/SDD §4.1 split-cluster phase routing:
//
//	1 preflight        — fan out, both contexts (handled separately; returns "")
//	2 resolve-stack    — no cluster contact (returns "")
//	3 render-cp        — control plane
//	4 apply-cp         — control plane
//	5 check-cp         — control plane (auth-gate)
//	6 register         — compute plane (reads compute-plane JWKS)
//	7 apply-compute    — compute plane
//	8 final-health     — control plane (NVCF API on control plane)
func kubectxFor(phaseNum int) string {
	if selfHostedControlPlaneContext == "" || selfHostedComputePlaneContext == "" {
		return ""
	}
	switch phaseNum {
	case 6, 7:
		return selfHostedComputePlaneContext
	case 3, 4, 5, 8:
		return selfHostedControlPlaneContext
	default:
		return ""
	}
}

// writeRegisterValuesYAML persists the helm values handoff file the worker-layer
// helmfile expects at <stack>/out/<cluster>-register-values.yaml. Schema mirrors
// `printRegistrationOutput` (cmd/cluster_registration.go, fixed in `ff9aaf7`):
// top-level `clusterID`/`clusterGroupID`/`ncaID` with the mixed-case "ID" suffix
// to match the nvca-operator chart's `## @param` annotations in
// `nvca-operator/values.yaml`. `selfManaged.identitySource` and endpoint
// values stay nested because they scope self-managed-only chart inputs alongside
// the chart's other `selfManaged:` keys (`nvcaVersion`, `region`, etc.).
//
// The pre-`ff9aaf7` shape (nested `selfManaged.clusterId`, lowercase-d) left
// the chart's `.Values.clusterID` empty → cluster-dto.yaml rendered with empty
// IDs → operator failed to fetch backend identity. (Iter #12 from dev-VM E2E.)
type registerValuesWriteRequest struct {
	StackPath      string
	ClusterName    string
	NCAID          string
	Region         string
	IdentitySource string
	ClusterID      string
	ClusterGroupID string
	Endpoints      registerEndpointValues
}

func writeRegisterValuesYAML(req registerValuesWriteRequest) error {
	outDir := filepath.Join(req.StackPath, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	vals := helmValues{
		ClusterName:    req.ClusterName,
		ClusterID:      req.ClusterID,
		ClusterGroupID: req.ClusterGroupID,
		NcaID:          req.NCAID,
		Region:         req.Region,
		SelfManaged:    newSelfManagedValuesFromEndpoints(req.IdentitySource, req.Endpoints),
	}
	body, err := yaml.Marshal(vals)
	if err != nil {
		return fmt.Errorf("marshal register values: %w", err)
	}
	for _, path := range []string{legacyRegisterValuesPath(req.StackPath, req.ClusterName), nvcaValuesPath(req.StackPath, req.ClusterName)} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func legacyRegisterValuesPath(stackPath, clusterName string) string {
	return filepath.Join(stackPath, "out", clusterName+"-register-values.yaml")
}

func nvcaValuesPath(stackPath, clusterName string) string {
	return filepath.Join(stackPath, "out", clusterName+"-nvca-values.yaml")
}
