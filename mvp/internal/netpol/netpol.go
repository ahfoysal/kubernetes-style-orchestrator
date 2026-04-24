// Package netpol evaluates NetworkPolicy objects for pod-to-pod traffic.
//
// Semantics (mirrors a simplified Kubernetes NetworkPolicy):
//
//   - A policy "selects" pods whose labels match spec.podSelector.
//   - Once any policy selects a pod, that pod becomes isolated: only traffic
//     explicitly allow-listed by at least one ingress rule is permitted.
//   - If no policy selects the destination pod, traffic is permitted
//     (default-allow for unselected pods — same as upstream k8s).
//
// The evaluator is pure: it takes the candidate source/destination pods
// and the full policy list and returns Allow / Deny. The kube-proxy
// consults this package on every inbound connection to decide whether
// to forward or drop.
package netpol

import (
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// LabelsMatch returns true if every (k,v) in selector is present in labels.
// An empty selector matches every pod.
func LabelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Decision is the outcome of evaluating a pod-to-pod connection.
type Decision struct {
	Allowed bool
	// Reason is a human-readable explanation, for logging.
	Reason string
	// Policy is the policy that decided the verdict (if any).
	Policy string
}

// Allow short-circuits to an allow decision.
func Allow(reason string) Decision { return Decision{Allowed: true, Reason: reason} }

// Deny short-circuits to a deny decision.
func Deny(policy, reason string) Decision {
	return Decision{Allowed: false, Reason: reason, Policy: policy}
}

// Evaluate returns the Decision for src → dst on dstPort.
//
//   - policies: full list from the apiserver.
//   - src, dst: pod objects (labels must be populated in Metadata).
//   - dstPort: the TCP port being accessed on dst.
//
// Rules:
//  1. Find every policy whose spec.podSelector matches dst. If none — allow.
//  2. For each such policy, walk ingress rules. A rule matches if
//     (From is empty OR any peer.podSelector matches src) AND
//     (Ports is empty OR any port equals dstPort).
//  3. If at least one rule matches, allow. Otherwise deny.
func Evaluate(policies []*api.NetworkPolicy, src, dst *api.Pod, dstPort int) Decision {
	if dst == nil {
		return Deny("", "no destination pod")
	}
	selecting := make([]*api.NetworkPolicy, 0, 4)
	for _, p := range policies {
		if p == nil {
			continue
		}
		if LabelsMatch(p.Spec.PodSelector, dst.Metadata.Labels) {
			selecting = append(selecting, p)
		}
	}
	if len(selecting) == 0 {
		return Allow("no policy selects destination pod")
	}
	// Any matching ingress rule in any selecting policy allows.
	var srcLabels map[string]string
	if src != nil {
		srcLabels = src.Metadata.Labels
	}
	for _, p := range selecting {
		for _, rule := range p.Spec.Ingress {
			if !peerAllows(rule.From, srcLabels) {
				continue
			}
			if !portAllows(rule.Ports, dstPort) {
				continue
			}
			return Decision{Allowed: true, Policy: p.Metadata.Name, Reason: "matched ingress rule"}
		}
	}
	return Decision{Allowed: false, Policy: selecting[0].Metadata.Name, Reason: "default-deny: no ingress rule matched"}
}

func peerAllows(from []api.NetPolPeer, srcLabels map[string]string) bool {
	if len(from) == 0 {
		return true // no peer constraint => any source
	}
	for _, peer := range from {
		if LabelsMatch(peer.PodSelector, srcLabels) {
			return true
		}
	}
	return false
}

func portAllows(ports []api.NetPolPort, port int) bool {
	if len(ports) == 0 {
		return true
	}
	for _, p := range ports {
		if p.Port == port {
			return true
		}
	}
	return false
}

// PodByHostPort returns the pod whose status.HostPort == port, or nil.
// The stub runtime assigns a unique loopback port per pod so (ip, port)
// uniquely identifies a pod within a node.
func PodByHostPort(pods []*api.Pod, port int) *api.Pod {
	for _, p := range pods {
		if p.Status.HostPort == port {
			return p
		}
	}
	return nil
}
