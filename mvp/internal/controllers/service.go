package controllers

import (
	"log"
	"sort"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

// ServiceController watches Services + Pods and maintains a matching
// Endpoints object per Service listing the IP:port of every Running pod
// whose labels match spec.selector.
type ServiceController struct {
	Client   *client.Client
	Interval time.Duration
}

func NewServiceController(c *client.Client, interval time.Duration) *ServiceController {
	return &ServiceController{Client: c, Interval: interval}
}

func (sc *ServiceController) Run(stop <-chan struct{}) {
	log.Printf("service-controller: starting (interval=%s)", sc.Interval)
	t := time.NewTicker(sc.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := sc.reconcileAll(); err != nil {
				log.Printf("service-controller: %v", err)
			}
		}
	}
}

func (sc *ServiceController) reconcileAll() error {
	svcs, err := sc.Client.ListServices()
	if err != nil {
		return err
	}
	pods, err := sc.Client.ListPods()
	if err != nil {
		return err
	}
	for _, svc := range svcs {
		sc.reconcileOne(svc, pods)
	}
	return nil
}

// selectorMatches returns true if every key/value in sel is present on labels.
func selectorMatches(sel, labels map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (sc *ServiceController) reconcileOne(svc *api.Service, pods []*api.Pod) {
	var subsets []api.EndpointAddress
	// Resolve TCP target port: first service port's targetPort (or port).
	targetPort := 0
	if len(svc.Spec.Ports) > 0 {
		targetPort = svc.Spec.Ports[0].TargetPort
		if targetPort == 0 {
			targetPort = svc.Spec.Ports[0].Port
		}
	}
	for _, p := range pods {
		if p.Status.Phase != api.PhaseRunning {
			continue
		}
		if !selectorMatches(svc.Spec.Selector, p.Metadata.Labels) {
			continue
		}
		ip := p.Status.PodIP
		port := p.Status.HostPort
		if port == 0 {
			// Fall back to the declared container port if runtime didn't
			// publish a hostPort (e.g. real docker runtime later).
			port = targetPort
		}
		if ip == "" || port == 0 {
			continue
		}
		subsets = append(subsets, api.EndpointAddress{
			IP:       ip,
			Port:     port,
			NodeName: p.Status.NodeName,
			PodName:  p.Metadata.Name,
		})
	}
	sort.Slice(subsets, func(i, j int) bool {
		if subsets[i].IP == subsets[j].IP {
			return subsets[i].Port < subsets[j].Port
		}
		return subsets[i].IP < subsets[j].IP
	})
	ep := &api.Endpoints{
		APIVersion: "v1",
		Kind:       "Endpoints",
		Metadata: api.Metadata{
			Name:      svc.Metadata.Name,
			Namespace: svc.Metadata.Namespace,
		},
		Subsets: subsets,
	}
	if err := sc.Client.UpsertEndpoints(ep); err != nil {
		log.Printf("service-controller: service=%s upsert endpoints failed: %v", svc.Metadata.Name, err)
		return
	}
	log.Printf("service-controller: service=%s clusterIP=%s endpoints=%d", svc.Metadata.Name, svc.Spec.ClusterIP, len(subsets))
}
