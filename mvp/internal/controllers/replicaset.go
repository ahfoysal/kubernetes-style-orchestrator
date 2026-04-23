// Package controllers holds the reconciliation loops for the higher-level
// resources (ReplicaSet, Deployment) in the style of Kubernetes'
// controller-manager.
package controllers

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

// LabelReplicaSet is the label written onto child pods so the controller
// can identify which RS owns them. Analogous to the
// ownerReferences/controller-ref pattern in Kubernetes.
const LabelReplicaSet = "mk.replicaset"

// ReplicaSetController reconciles ReplicaSet objects toward the desired
// replica count by creating or deleting Pods.
type ReplicaSetController struct {
	Client   *client.Client
	Interval time.Duration
}

func NewReplicaSetController(c *client.Client, interval time.Duration) *ReplicaSetController {
	return &ReplicaSetController{Client: c, Interval: interval}
}

func (rc *ReplicaSetController) Run(stop <-chan struct{}) {
	log.Printf("replicaset-controller: starting (interval=%s)", rc.Interval)
	t := time.NewTicker(rc.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := rc.reconcileAll(); err != nil {
				log.Printf("replicaset-controller: %v", err)
			}
		}
	}
}

func (rc *ReplicaSetController) reconcileAll() error {
	rsList, err := rc.Client.ListReplicaSets()
	if err != nil {
		return err
	}
	pods, err := rc.Client.ListPods()
	if err != nil {
		return err
	}
	for _, rs := range rsList {
		rc.reconcileOne(rs, pods)
	}
	return nil
}

func (rc *ReplicaSetController) reconcileOne(rs *api.ReplicaSet, allPods []*api.Pod) {
	owned := ownedPods(rs.Metadata.Name, allPods)
	alive := make([]*api.Pod, 0, len(owned))
	for _, p := range owned {
		if p.Status.Phase == api.PhaseFailed {
			continue
		}
		alive = append(alive, p)
	}

	desired := rs.Spec.Replicas
	actual := len(alive)

	switch {
	case actual < desired:
		for i := 0; i < desired-actual; i++ {
			p := buildPodFromTemplate(rs)
			if _, err := rc.Client.CreatePod(p); err != nil {
				log.Printf("replicaset-controller: rs=%s create pod failed: %v", rs.Metadata.Name, err)
				continue
			}
			log.Printf("replicaset-controller: rs=%s created pod=%s", rs.Metadata.Name, p.Metadata.Name)
		}
	case actual > desired:
		// Delete the newest pods first — deterministic-ish.
		victims := alive[desired:]
		for _, p := range victims {
			if err := rc.Client.DeletePod(p.Metadata.Name); err != nil {
				log.Printf("replicaset-controller: rs=%s delete pod=%s failed: %v", rs.Metadata.Name, p.Metadata.Name, err)
				continue
			}
			log.Printf("replicaset-controller: rs=%s deleted pod=%s", rs.Metadata.Name, p.Metadata.Name)
		}
	}

	// Update status subresource with observed counts.
	ready := 0
	for _, p := range alive {
		if p.Status.Phase == api.PhaseRunning {
			ready++
		}
	}
	_ = rc.Client.UpdateReplicaSetStatus(rs.Metadata.Name, api.ReplicaSetStatus{
		Replicas:      len(alive),
		ReadyReplicas: ready,
	})
}

// ownedPods filters to pods that carry the RS label pointing at rsName.
func ownedPods(rsName string, pods []*api.Pod) []*api.Pod {
	out := make([]*api.Pod, 0)
	for _, p := range pods {
		if p.Metadata.Labels == nil {
			continue
		}
		if p.Metadata.Labels[LabelReplicaSet] == rsName {
			out = append(out, p)
		}
	}
	return out
}

func buildPodFromTemplate(rs *api.ReplicaSet) *api.Pod {
	suffix := randSuffix(5)
	name := rs.Metadata.Name + "-" + suffix
	labels := map[string]string{}
	for k, v := range rs.Spec.Template.Metadata.Labels {
		labels[k] = v
	}
	for k, v := range rs.Metadata.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}
	labels[LabelReplicaSet] = rs.Metadata.Name

	return &api.Pod{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: api.Metadata{
			Name:      name,
			Namespace: rs.Metadata.Namespace,
			Labels:    labels,
		},
		Spec: rs.Spec.Template.Spec,
	}
}

func randSuffix(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
