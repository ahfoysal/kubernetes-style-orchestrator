// Package hpa implements the HorizontalPodAutoscaler controller.
//
// On each tick the controller:
//  1. Lists HPAs.
//  2. For each HPA, looks up its target Deployment + owned pods.
//  3. Fetches current CPU % from the metrics-server (/apis/metrics.mk.io/v1/pods).
//  4. Computes desiredReplicas = ceil(currentAvg / targetAvg * currentReplicas),
//     clamped to [min, max].
//  5. If desired != current, writes Deployment.spec.replicas and updates
//     HPA status.
//
// Scaling formula matches Kubernetes' HPA algorithm (simplified, no
// stabilization windows).
package hpa

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/controllers"
)

// Controller reconciles HPAs.
type Controller struct {
	Client       *client.Client
	MetricsURL   string
	Interval     time.Duration
	CooldownDown time.Duration
}

// New creates an HPA controller. metricsURL is the metrics-server base URL.
func New(c *client.Client, metricsURL string, interval time.Duration) *Controller {
	return &Controller{
		Client:       c,
		MetricsURL:   metricsURL,
		Interval:     interval,
		CooldownDown: 15 * time.Second,
	}
}

// Run loops until stop is closed.
func (c *Controller) Run(stop <-chan struct{}) {
	log.Printf("hpa-controller: starting (interval=%s metrics=%s)", c.Interval, c.MetricsURL)
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := c.reconcileAll(); err != nil {
				log.Printf("hpa-controller: %v", err)
			}
		}
	}
}

func (c *Controller) reconcileAll() error {
	hpas, err := c.Client.ListHPAs()
	if err != nil {
		return err
	}
	metrics, err := c.fetchMetrics()
	if err != nil {
		log.Printf("hpa-controller: fetch metrics: %v (falling back to zero usage)", err)
		metrics = nil
	}
	for _, h := range hpas {
		c.reconcileOne(h, metrics)
	}
	return nil
}

func (c *Controller) reconcileOne(h *api.HPA, metrics []api.PodMetric) {
	if h.Spec.ScaleTargetRef.Kind != "Deployment" {
		return
	}
	dep, err := c.getDeployment(h.Spec.ScaleTargetRef.Name)
	if err != nil {
		log.Printf("hpa=%s: get deployment %s: %v", h.Metadata.Name, h.Spec.ScaleTargetRef.Name, err)
		return
	}
	// Find owned pods (via ReplicaSet label) and average their CPU.
	pods, err := c.Client.ListPods()
	if err != nil {
		return
	}
	rsName := dep.Metadata.Name + "-rs"
	var owned []*api.Pod
	for _, p := range pods {
		if p.Metadata.Labels != nil && p.Metadata.Labels[controllers.LabelReplicaSet] == rsName {
			owned = append(owned, p)
		}
	}
	current := dep.Spec.Replicas
	avg := averageCPU(owned, metrics)

	target := h.Spec.Metric.TargetAverageValue
	if target <= 0 {
		target = 50
	}
	// desired = ceil(currentPods * currentAvg / target)
	desired := current
	if current > 0 && avg > 0 {
		ratio := avg / float64(target)
		desired = int(math.Ceil(float64(current) * ratio))
	} else if avg == 0 && current > h.Spec.MinReplicas {
		desired = h.Spec.MinReplicas
	}
	if desired < h.Spec.MinReplicas {
		desired = h.Spec.MinReplicas
	}
	if desired > h.Spec.MaxReplicas {
		desired = h.Spec.MaxReplicas
	}

	status := api.HPAStatus{
		CurrentReplicas:     current,
		DesiredReplicas:     desired,
		CurrentAverageValue: int(avg),
		LastScaleTime:       h.Status.LastScaleTime,
	}
	if desired != current {
		// Apply a simple scale-down cooldown to avoid flapping.
		if desired < current && !h.Status.LastScaleTime.IsZero() && time.Since(h.Status.LastScaleTime) < c.CooldownDown {
			log.Printf("hpa=%s: scale-down cooldown, keeping replicas=%d (wanted %d)", h.Metadata.Name, current, desired)
		} else {
			if err := c.scaleDeployment(dep, desired); err != nil {
				log.Printf("hpa=%s: scale %s -> %d failed: %v", h.Metadata.Name, dep.Metadata.Name, desired, err)
			} else {
				log.Printf("hpa=%s: scaled %s %d -> %d (avgCPU=%.1f%% target=%d%%)", h.Metadata.Name, dep.Metadata.Name, current, desired, avg, target)
				status.LastScaleTime = time.Now().UTC()
				status.CurrentReplicas = desired
			}
		}
	}
	_ = c.Client.UpdateHPAStatus(h.Metadata.Name, status)
}

func averageCPU(pods []*api.Pod, metrics []api.PodMetric) float64 {
	if len(pods) == 0 {
		return 0
	}
	byName := map[string]float64{}
	for _, m := range metrics {
		byName[m.PodName] = m.CPUPct
	}
	var sum float64
	var n int
	for _, p := range pods {
		if v, ok := byName[p.Metadata.Name]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func (c *Controller) fetchMetrics() ([]api.PodMetric, error) {
	if c.MetricsURL == "" {
		return nil, fmt.Errorf("metrics URL unset")
	}
	r, err := http.Get(c.MetricsURL + "/apis/metrics.mk.io/v1/pods")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("%s: %s", r.Status, string(b))
	}
	var out struct {
		Items []api.PodMetric `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Controller) getDeployment(name string) (*api.Deployment, error) {
	// The client doesn't expose GetDeployment; list and filter.
	deps, err := c.Client.ListDeployments()
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		if d.Metadata.Name == name {
			return d, nil
		}
	}
	return nil, fmt.Errorf("deployment %s not found", name)
}

// scaleDeployment posts an updated Deployment with new replica count. The
// apiserver upserts preserve the Deployment's status.
func (c *Controller) scaleDeployment(d *api.Deployment, replicas int) error {
	copy := *d
	copy.Spec.Replicas = replicas
	_, err := c.Client.CreateDeployment(&copy)
	return err
}
