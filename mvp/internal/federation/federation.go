// Package federation implements the FederatedDeployment controller and
// the cluster-health probe used by the control plane.
//
// A FederatedDeployment fans a single Deployment spec out to every
// registered Cluster (or a named subset). The controller talks to each
// member cluster's apiserver over REST and upserts a derived Deployment
// object there. Status reports how many clusters are in sync.
//
// Cluster health: every tick we GET /healthz on each member cluster and
// stamp Status.Ready + LastHeartbeat.
package federation

import (
	"log"
	"net/http"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

// Controller runs the federation reconciliation loop.
type Controller struct {
	// Host client talks to the federation control-plane apiserver.
	Host *client.Client
	// Interval between reconciliation ticks.
	Interval time.Duration
	// Probe is the http.Client used for cluster /healthz probes.
	Probe *http.Client
}

// New builds a Controller.
func New(host *client.Client, interval time.Duration) *Controller {
	return &Controller{
		Host:     host,
		Interval: interval,
		Probe:    &http.Client{Timeout: 2 * time.Second},
	}
}

// Run blocks, reconciling until stop closes.
func (c *Controller) Run(stop <-chan struct{}) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := c.reconcile(); err != nil {
				log.Printf("federation: reconcile error: %v", err)
			}
		}
	}
}

func (c *Controller) reconcile() error {
	clusters, err := c.Host.ListClusters()
	if err != nil {
		return err
	}
	// 1. Probe every cluster's /healthz, update its status.
	for _, cl := range clusters {
		ok, msg := c.probe(cl.Spec.Server)
		st := api.ClusterStatus{Ready: ok, Message: msg}
		if err := c.Host.UpdateClusterStatus(cl.Metadata.Name, st); err != nil {
			log.Printf("federation: update cluster %s status: %v", cl.Metadata.Name, err)
		}
		cl.Status.Ready = ok
	}

	// 2. Reconcile every FederatedDeployment.
	fds, err := c.Host.ListFederatedDeployments()
	if err != nil {
		return err
	}
	for _, fd := range fds {
		targets := selectClusters(fd, clusters)
		ready := 0
		for _, cl := range targets {
			if !cl.Status.Ready {
				continue
			}
			memberCli := client.New(cl.Spec.Server)
			dep := &api.Deployment{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Metadata: api.Metadata{
					Name:      fd.Metadata.Name,
					Namespace: fd.Metadata.Namespace,
					Labels: mergeLabels(fd.Metadata.Labels, map[string]string{
						"federation.mk.io/federated-deployment": fd.Metadata.Name,
						"federation.mk.io/cluster":              cl.Metadata.Name,
					}),
				},
				Spec: fd.Spec.Template,
			}
			if _, err := memberCli.CreateDeployment(dep); err != nil {
				log.Printf("federation: push %s to cluster %s failed: %v", fd.Metadata.Name, cl.Metadata.Name, err)
				continue
			}
			ready++
		}
		_ = c.Host.UpdateFederatedDeploymentStatus(fd.Metadata.Name, api.FederatedDeploymentStatus{
			ClusterCount: len(targets),
			ReadyCount:   ready,
		})
	}
	return nil
}

func (c *Controller) probe(server string) (bool, string) {
	r, err := c.Probe.Get(server + "/healthz")
	if err != nil {
		return false, err.Error()
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		return false, r.Status
	}
	return true, ""
}

// selectClusters returns the clusters this FD should target.
func selectClusters(fd *api.FederatedDeployment, all []*api.Cluster) []*api.Cluster {
	if len(fd.Spec.Clusters) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, n := range fd.Spec.Clusters {
		want[n] = true
	}
	out := make([]*api.Cluster, 0, len(fd.Spec.Clusters))
	for _, c := range all {
		if want[c.Metadata.Name] {
			out = append(out, c)
		}
	}
	return out
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
