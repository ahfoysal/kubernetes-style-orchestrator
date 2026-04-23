package controllers

import (
	"log"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

// LabelDeployment is set on the owned ReplicaSet so the deployment
// controller can locate its current RS (similar to K8s' controller-ref).
const LabelDeployment = "mk.deployment"

// DeploymentController reconciles Deployment objects into a single
// ReplicaSet with matching spec/replicas. Rolling-update strategy is
// stubbed for M3; current semantics are Recreate (single RS per Deployment).
type DeploymentController struct {
	Client   *client.Client
	Interval time.Duration
}

func NewDeploymentController(c *client.Client, interval time.Duration) *DeploymentController {
	return &DeploymentController{Client: c, Interval: interval}
}

func (dc *DeploymentController) Run(stop <-chan struct{}) {
	log.Printf("deployment-controller: starting (interval=%s)", dc.Interval)
	t := time.NewTicker(dc.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := dc.reconcileAll(); err != nil {
				log.Printf("deployment-controller: %v", err)
			}
		}
	}
}

func (dc *DeploymentController) reconcileAll() error {
	deps, err := dc.Client.ListDeployments()
	if err != nil {
		return err
	}
	for _, d := range deps {
		dc.reconcileOne(d)
	}
	return nil
}

func (dc *DeploymentController) reconcileOne(d *api.Deployment) {
	rsName := d.Metadata.Name + "-rs"

	labels := map[string]string{}
	for k, v := range d.Metadata.Labels {
		labels[k] = v
	}
	labels[LabelDeployment] = d.Metadata.Name

	// Template labels flow through to pods.
	tmplLabels := map[string]string{}
	for k, v := range d.Spec.Template.Metadata.Labels {
		tmplLabels[k] = v
	}
	tmplLabels[LabelDeployment] = d.Metadata.Name

	rs := &api.ReplicaSet{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Metadata: api.Metadata{
			Name:      rsName,
			Namespace: d.Metadata.Namespace,
			Labels:    labels,
		},
		Spec: api.ReplicaSetSpec{
			Replicas: d.Spec.Replicas,
			Selector: d.Spec.Selector,
			Template: api.PodTemplate{
				Metadata: api.Metadata{
					Labels: tmplLabels,
				},
				Spec: d.Spec.Template.Spec,
			},
		},
	}

	existing, err := dc.Client.GetReplicaSet(rsName)
	if err != nil {
		// Assume not found — create.
		if _, cerr := dc.Client.CreateReplicaSet(rs); cerr != nil {
			log.Printf("deployment-controller: deployment=%s create rs failed: %v", d.Metadata.Name, cerr)
			return
		}
		log.Printf("deployment-controller: deployment=%s created rs=%s replicas=%d", d.Metadata.Name, rsName, rs.Spec.Replicas)
	} else if existing.Spec.Replicas != d.Spec.Replicas {
		// Re-upsert to update desired replicas. Upsert preserves status.
		if _, cerr := dc.Client.CreateReplicaSet(rs); cerr != nil {
			log.Printf("deployment-controller: deployment=%s update rs failed: %v", d.Metadata.Name, cerr)
			return
		}
		log.Printf("deployment-controller: deployment=%s scaled rs=%s replicas=%d", d.Metadata.Name, rsName, rs.Spec.Replicas)
	}

	// Propagate RS status up to the Deployment status subresource.
	if cur, err := dc.Client.GetReplicaSet(rsName); err == nil {
		_ = dc.Client.UpdateDeploymentStatus(d.Metadata.Name, api.DeploymentStatus{
			Replicas:      cur.Status.Replicas,
			ReadyReplicas: cur.Status.ReadyReplicas,
		})
	}
}
