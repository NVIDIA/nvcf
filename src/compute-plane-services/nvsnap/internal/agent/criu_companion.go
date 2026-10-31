// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Instance companions. An engine's state is not always its own: a Dynamo
// worker holds a lease in its instance's etcd and keeps registrations
// there, and a restored worker whose lease is gone shuts down. So an
// instance's capture also takes the pods without GPUs that its GPU ranks
// hold connections to in their namespace (the companions: an etcd, a
// NATS), and its restore brings them back with the ranks. A pod that
// connects to those companions shares the instance's state too (a Dynamo
// frontend keeps its own lease and registrations in the etcd, and reaches
// the workers at the addresses they registered there, which only restored
// processes can), so it is a companion as well. A pod that only connects
// to the ranks is a client of the instance (a request proxy), and NVCA's
// own pods never belong to it (nvcaPlatformPod).
//
// Restored processes run in a time namespace that continues their clocks
// from the capture (CRIU restores the dumped clock offsets), so neither a
// worker nor its etcd sees the time the instance was down. A clock starts
// again at its process's restore, though, and a GPU rank's reload of its GPU
// memory (minutes for a large model) runs with the rank frozen. So the
// companions are restored only once every rank has reloaded, with the
// instance's network locked until the last of them is restored: an etcd
// then never counts the reload against the leases the ranks could not
// renew (gpushare_group_restore.go).
//
// The members reach a companion through a Service, whose address is new in
// the new instance and leads to the new pods only once they are Ready. A
// connection through a Service that led to one member at capture is
// restored straight to that member's new address (serviceAliases), so it
// survives the restore: a lease held over it does not have to outlast the
// new Service's endpoints. Every other connection to a peer outside the
// group is closed once restored (resetDeadPeers), so the engine sees it fail
// at once and reconnects by name.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// isCompanion reports a pod admitted as a group restore's companion.
func isCompanion(p *corev1.Pod) bool {
	return p.Annotations[webhook.CRIUGroupCompanionAnnotation] == "true"
}

// groupMember reports whether p belongs to leader's instance: the same
// instance by its workload labels, or a companion of the same group
// restore.
func groupMember(leader, p *corev1.Pod) bool {
	if sameInstance(leader, p) {
		return true
	}
	key := leader.Annotations[webhook.CRIUGroupAnnotation]
	return key != "" && isCompanion(p) && p.Namespace == leader.Namespace && p.Annotations[webhook.CRIUGroupAnnotation] == key
}

// hasGPU reports a pod that requests GPUs.
func hasGPU(p *corev1.Pod) bool { return gpuContainer(p) != "" }

// discoverCompanions returns the companions of an instance: the pods of
// its namespace without GPUs that its ranks hold TCP connections to,
// directly or through a Service, and the pods that connect to those, in a
// stable order, and the container to capture of each (companionContainer).
// It fails when one cannot be captured, rather than capture an instance
// without it.
func (a *Agent) discoverCompanions(ctx context.Context, ranks []*corev1.Pod, log *logrus.Entry) ([]*corev1.Pod, map[string]string, error) {
	ns := ranks[0].Namespace
	peers := map[string]bool{}
	for _, r := range ranks {
		base, err := a.peerAgentURL(ctx, r.Spec.NodeName)
		if err != nil {
			return nil, nil, fmt.Errorf("rank %s: %w", r.Name, err)
		}
		ips, err := fetchPeers(ctx, base, r.Namespace, r.Name, gpuContainer(r), false)
		if err != nil {
			return nil, nil, fmt.Errorf("rank %s: connections: %w", r.Name, err)
		}
		for _, ip := range ips {
			peers[ip] = true
		}
	}
	if len(peers) == 0 {
		return nil, nil, nil
	}
	pods, err := a.kubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	svcs, err := a.kubeClient.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	sl, err := a.kubeClient.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	slices := sl.Items
	found := matchCompanions(peers, pods.Items, svcs.Items, slices, ranks)
	if len(found) > 0 {
		reach := companionAddresses(found, svcs.Items, slices)
		peersOf := map[string][]string{}
		for _, p := range companionCandidates(pods.Items, ranks) {
			if found[p.Name] != nil || p.Status.PodIP == "" || p.Spec.NodeName == "" {
				continue
			}
			base, err := a.peerAgentURL(ctx, p.Spec.NodeName)
			if err != nil {
				return nil, nil, fmt.Errorf("pod %s: %w", p.Name, err)
			}
			for _, c := range p.Spec.Containers {
				ips, err := fetchPeers(ctx, base, p.Namespace, p.Name, c.Name, false)
				if err != nil {
					return nil, nil, fmt.Errorf("pod %s: container %s: connections: %w", p.Name, c.Name, err)
				}
				peersOf[p.Name] = append(peersOf[p.Name], ips...)
			}
		}
		for name, p := range clientsOf(reach, peersOf, pods.Items) {
			found[name] = p
		}
	}
	instance := companionAddresses(found, svcs.Items, slices)
	for _, r := range ranks {
		for _, ip := range podIPs(r) {
			instance[ip] = true
		}
	}
	out := make([]*corev1.Pod, 0, len(found))
	containers := map[string]string{}
	for _, p := range found {
		if err := a.companionCapturable(ctx, p); err != nil {
			return nil, nil, fmt.Errorf("companion %s: %w", p.Name, err)
		}
		c, err := a.companionContainer(ctx, p, instance)
		if err != nil {
			return nil, nil, fmt.Errorf("companion %s: %w", p.Name, err)
		}
		containers[p.Name] = c
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > 0 {
		names := make([]string, len(out))
		for i, p := range out {
			names[i] = p.Name + "/" + containers[p.Name]
		}
		log.WithField("companions", strings.Join(names, ",")).Info("CRIU group capture: the instance's companions")
	}
	return out, containers, nil
}

// companionContainer is the container of companion p to capture: its only
// one, or the one whose own processes hold connections, in either
// direction, to the instance (instance: the addresses of its ranks and
// companions and of the Services that lead to them). The other containers
// of the pod (a readiness sidecar, say) start fresh in the restored pod, as
// a rank's sidecars do.
func (a *Agent) companionContainer(ctx context.Context, p *corev1.Pod, instance map[string]bool) (string, error) {
	if len(p.Spec.Containers) == 1 {
		return p.Spec.Containers[0].Name, nil
	}
	base, err := a.peerAgentURL(ctx, p.Spec.NodeName)
	if err != nil {
		return "", err
	}
	var talking []string
	for _, c := range p.Spec.Containers {
		ips, err := fetchPeers(ctx, base, p.Namespace, p.Name, c.Name, true)
		if err != nil {
			return "", fmt.Errorf("container %s: connections: %w", c.Name, err)
		}
		if anyIn(ips, instance) {
			talking = append(talking, c.Name)
		}
	}
	return pickCompanionContainer(len(p.Spec.Containers), talking)
}

// pickCompanionContainer chooses among a companion's containers: exactly
// one of them may hold connections to the instance.
func pickCompanionContainer(containers int, talking []string) (string, error) {
	switch len(talking) {
	case 1:
		return talking[0], nil
	case 0:
		return "", fmt.Errorf("none of its %d containers holds a connection to the instance", containers)
	default:
		return "", fmt.Errorf("containers %s all hold connections to the instance; a companion is captured with one", strings.Join(talking, ", "))
	}
}

func anyIn(ips []string, set map[string]bool) bool {
	for _, ip := range ips {
		if set[ip] {
			return true
		}
	}
	return false
}

// companionCandidates returns the pods of the namespace that can be
// companions: without GPUs, not a rank, not being deleted, not NVCA's.
func companionCandidates(pods []corev1.Pod, ranks []*corev1.Pod) []*corev1.Pod {
	rank := map[string]bool{}
	for _, r := range ranks {
		rank[r.Name] = true
	}
	var out []*corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || rank[p.Name] || hasGPU(p) || nvcaPlatformPod(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// podIPs returns a pod's addresses.
func podIPs(p *corev1.Pod) []string {
	var out []string
	for _, ip := range p.Status.PodIPs {
		out = append(out, ip.IP)
	}
	if p.Status.PodIP != "" {
		out = append(out, p.Status.PodIP)
	}
	return out
}

// matchCompanions maps the ranks' peer addresses to the companion
// candidates of their namespace: a pod's own address, or a Service's
// cluster address and the pods of its endpoints.
func matchCompanions(peers map[string]bool, pods []corev1.Pod, svcs []corev1.Service, slices []discoveryv1.EndpointSlice, ranks []*corev1.Pod) map[string]*corev1.Pod {
	byIP, byName := map[string]*corev1.Pod{}, map[string]*corev1.Pod{}
	for _, p := range companionCandidates(pods, ranks) {
		byName[p.Name] = p
		for _, ip := range podIPs(p) {
			byIP[ip] = p
		}
	}
	found := map[string]*corev1.Pod{}
	for ip := range peers {
		if p := byIP[ip]; p != nil {
			found[p.Name] = p
		}
	}
	served := map[string]bool{}
	for _, svc := range svcs {
		if servesAny(svc, peers) {
			served[svc.Name] = true
		}
	}
	for _, sl := range slices {
		if !served[sl.Labels[discoveryv1.LabelServiceName]] {
			continue
		}
		for _, ep := range sl.Endpoints {
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
				continue
			}
			if p := byName[ep.TargetRef.Name]; p != nil {
				found[p.Name] = p
			}
		}
	}
	return found
}

// companionAddresses returns the addresses that lead to the companions:
// their own and the cluster addresses of the Services whose endpoints
// include one of them.
func companionAddresses(companions map[string]*corev1.Pod, svcs []corev1.Service, slices []discoveryv1.EndpointSlice) map[string]bool {
	out := map[string]bool{}
	for _, p := range companions {
		for _, ip := range podIPs(p) {
			out[ip] = true
		}
	}
	leads := map[string]bool{}
	for _, sl := range slices {
		for _, ep := range sl.Endpoints {
			if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" && companions[ep.TargetRef.Name] != nil {
				leads[sl.Labels[discoveryv1.LabelServiceName]] = true
			}
		}
	}
	for _, svc := range svcs {
		if !leads[svc.Name] {
			continue
		}
		for _, ip := range append([]string{svc.Spec.ClusterIP}, svc.Spec.ClusterIPs...) {
			if ip != "" && ip != corev1.ClusterIPNone {
				out[ip] = true
			}
		}
	}
	return out
}

// clientsOf returns the pods of peersOf (a pod's name to the addresses it
// opened connections to) that connect to an address of reach: the pods
// sharing the companions' state.
func clientsOf(reach map[string]bool, peersOf map[string][]string, pods []corev1.Pod) map[string]*corev1.Pod {
	out := map[string]*corev1.Pod{}
	for i := range pods {
		p := &pods[i]
		if ips, ok := peersOf[p.Name]; ok && anyIn(ips, reach) {
			out[p.Name] = p
		}
	}
	return out
}

// servesAny reports whether one of a Service's cluster addresses is among
// peers.
func servesAny(svc corev1.Service, peers map[string]bool) bool {
	ips := append([]string{svc.Spec.ClusterIP}, svc.Spec.ClusterIPs...)
	for _, ip := range ips {
		if ip != "" && ip != corev1.ClusterIPNone && peers[ip] {
			return true
		}
	}
	return false
}

// companionCapturable checks what a companion's capture and restore need:
// ready, run by a controller (so a replacement pod comes to restore into),
// on a node with an agent. companionContainer picks the container.
func (a *Agent) companionCapturable(ctx context.Context, p *corev1.Pod) error {
	if !rootfsonly.IsPodReady(p) {
		return fmt.Errorf("is not Ready")
	}
	if metav1.GetControllerOf(p) == nil {
		return fmt.Errorf("has no controller to replace it")
	}
	if a.specCacheURI == nil {
		return fmt.Errorf("the agent's webhook is off, so no configuration to restore it by")
	}
	if _, err := a.peerAgentURL(ctx, p.Spec.NodeName); err != nil {
		return fmt.Errorf("node %s: %w", p.Spec.NodeName, err)
	}
	return nil
}

// criuCompanionAdmits is the webhook's check before admitting a pod
// without GPUs as a companion placeholder: no GPU pod of its namespace
// already runs outside a restore. A companion recreated next to a running
// instance (its etcd crashed) must start, not wait for a restore.
func (a *Agent) criuCompanionAdmits(ctx context.Context, pod *corev1.Pod) (bool, string) {
	pods, err := a.kubeClient.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Sprintf("cannot list the namespace's pods: %v", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || !hasGPU(p) {
			continue
		}
		if p.Annotations[webhook.CRIUGroupAnnotation] != "" && p.Annotations[webhook.CRIURestoredAnnotation] == "" {
			continue // a placeholder of this restore
		}
		return false, fmt.Sprintf("GPU pod %s already runs; this pod replaces a companion of a running instance", p.Name)
	}
	return true, ""
}

// companionPlaceholderWait bounds how long a companion placeholder waits
// for its group's restore before it is released: its GPU ranks may have
// been admitted cold.
const companionPlaceholderWait = criuGroupPlaceholderWait + 5*time.Minute

// considerCompanionPlaceholder releases a companion placeholder no restore
// picked up, counting a stall on its record so the configuration starts
// cold after criuGroupMaxStalls of them.
func (c *criuAutoRestorer) considerCompanionPlaceholder(ctx context.Context, pod *corev1.Pod) {
	if !isCompanion(pod) || pod.Annotations[webhook.CRIURestoredAnnotation] != "" || pod.DeletionTimestamp != nil {
		return
	}
	if time.Since(pod.CreationTimestamp.Time) < companionPlaceholderWait {
		return
	}
	if _, busy := c.attempted.LoadOrStore("companion/"+string(pod.UID), struct{}{}); busy {
		return
	}
	key := pod.Annotations[webhook.CRIUGroupAnnotation]
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "criuGroup": key})
	c.failGroup(ctx, pod, key, false, fmt.Errorf("companion placeholder not restored after %s", companionPlaceholderWait), log)
}

// peersHandler lists the peers of a pod's established TCP connections that
// it opened, or of all of them with direction=both, for the agent that
// captures its instance.
func (a *Agent) peersHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	info, err := a.runtime.FindContainerByPod(r.Context(), v["namespace"], v["pod"], r.URL.Query().Get("container"))
	if err != nil || info == nil || info.PID == 0 {
		http.Error(w, fmt.Sprintf("container not found: %v", err), http.StatusNotFound)
		return
	}
	log := a.log.WithFields(logrus.Fields{"pod": v["namespace"] + "/" + v["pod"], "container": r.URL.Query().Get("container")})
	both := r.URL.Query().Get("direction") == "both"
	ips, err := tcpPeersOfContainer(int(info.PID), both, log)
	if err != nil {
		log.WithError(err).Warn("connections of the container unreadable; using the pod's network namespace")
		ips, err = tcpPeersInNS(int(info.PID), both, log)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func fetchPeers(ctx context.Context, base, ns, pod, container string, both bool) ([]string, error) {
	u := fmt.Sprintf("%s/v1/pods/%s/%s/peers?container=%s", base, url.PathEscape(ns), url.PathEscape(pod), url.QueryEscape(container))
	if both {
		u += "&direction=both"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := fabricHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var ips []string
	return ips, json.Unmarshal(b, &ips)
}

// resetDeadPeers closes, in a restored pod's network namespace, the
// connections whose peer is not part of the restored group: their peer's
// address belonged to the captured instance and no longer answers, and an
// engine would otherwise wait for a timeout before it reconnects. keep
// holds the addresses of the group's restored pods.
func resetDeadPeers(hostPID int, keep map[string]bool, log *logrus.Entry) {
	n, err := destroyTCPInNS(hostPID, func(remote net.IP) bool {
		return remote.IsLoopback() || keep[remote.String()]
	}, "CRIU restore: closed a connection to a peer outside the restored group", log)
	if err != nil {
		log.WithError(err).Warn("CRIU restore: cannot close the connections to peers outside the group")
		return
	}
	if n > 0 {
		log.WithField("closed", n).Info("CRIU restore: connections to peers outside the group closed; the engine reconnects")
	}
}

// keepAddresses is the set of addresses a restored pod keeps connections
// to: its own and every new address of an inet address map
// ("OLD=NEW,...").
func keepAddresses(own string, inetAddrMap string) map[string]bool {
	keep := map[string]bool{}
	if ip := net.ParseIP(own); ip != nil {
		keep[ip.String()] = true
	}
	for _, pair := range strings.Split(inetAddrMap, ",") {
		if _, nw, ok := strings.Cut(pair, "="); ok {
			if ip := net.ParseIP(strings.TrimSpace(nw)); ip != nil {
				keep[ip.String()] = true
			}
		}
	}
	return keep
}

// hostProcBase is the node's /proc as the agent sees it.
func hostProcBase() string {
	if _, err := os.Stat("/host/proc"); err == nil {
		return "/host/proc"
	}
	return "/proc"
}

// serviceAliases returns the cluster addresses of the namespace's Services
// that lead to exactly one pod of the instance (members), with no port
// translation, each with that pod's address ("CLUSTERIP=PODIP,..."). A
// connection a member holds through such a Service is restored straight to
// the pod that serves it, so it survives the restore: the new instance's
// Service only leads to its pods once they are Ready, later than a lease
// held over the connection allows. Any other Service's connections are
// closed at restore and reconnect.
func serviceAliases(svcs []corev1.Service, slices []discoveryv1.EndpointSlice, members []*corev1.Pod) string {
	memberIP := map[string]string{}
	for _, p := range members {
		if p.Status.PodIP != "" {
			memberIP[p.Name] = p.Status.PodIP
		}
	}
	bySvc := map[string][]discoveryv1.EndpointSlice{}
	for _, sl := range slices {
		name := sl.Labels[discoveryv1.LabelServiceName]
		bySvc[name] = append(bySvc[name], sl)
	}
	var out []string
	for _, svc := range svcs {
		ip := svc.Spec.ClusterIP
		if ip == "" || ip == corev1.ClusterIPNone || net.ParseIP(ip) == nil {
			continue
		}
		target, ok := soleMemberEndpoint(svc, bySvc[svc.Name], memberIP)
		if !ok {
			continue
		}
		out = append(out, ip+"="+target)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// soleMemberEndpoint returns the address of the one ready endpoint of svc,
// when it is a member's pod and every port of svc reaches it on the same
// number.
func soleMemberEndpoint(svc corev1.Service, slices []discoveryv1.EndpointSlice, memberIP map[string]string) (string, bool) {
	target := ""
	for _, sl := range slices {
		for _, ep := range sl.Endpoints {
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" || memberIP[ep.TargetRef.Name] == "" {
				return "", false
			}
			ip := memberIP[ep.TargetRef.Name]
			if target != "" && target != ip {
				return "", false
			}
			target = ip
		}
		for _, sp := range svc.Spec.Ports {
			for _, ep := range sl.Ports {
				if ep.Port != nil && (ep.Name == nil && sp.Name == "" || ep.Name != nil && *ep.Name == sp.Name) && *ep.Port != sp.Port {
					return "", false
				}
			}
		}
	}
	return target, target != ""
}

// aliasAddrMap extends an inet address map ("OLD=NEW,...") with the
// Service aliases recorded at capture ("CLUSTERIP=OLDPODIP,..."): each
// cluster address leads to the new address of the pod that served it.
func aliasAddrMap(addrMap, aliases string) string {
	moved := map[string]string{}
	for _, pair := range strings.Split(addrMap, ",") {
		if o, n, ok := strings.Cut(pair, "="); ok {
			moved[strings.TrimSpace(o)] = strings.TrimSpace(n)
		}
	}
	out := addrMap
	for _, pair := range strings.Split(aliases, ",") {
		cip, pod, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		cip = strings.TrimSpace(cip)
		if nw := moved[strings.TrimSpace(pod)]; nw != "" && moved[cip] == "" {
			out += "," + cip + "=" + nw
			moved[cip] = nw
		}
	}
	return out
}

// instanceServiceAliases lists the Services of the members' namespace and
// returns their serviceAliases; "" when they cannot be listed (the
// connections through them are then closed at restore, as for any other
// Service).
func (a *Agent) instanceServiceAliases(ctx context.Context, members []*corev1.Pod) string {
	if len(members) == 0 {
		return ""
	}
	ns := members[0].Namespace
	svcs, err := a.kubeClient.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	slices, err := a.kubeClient.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	return serviceAliases(svcs.Items, slices.Items, members)
}
