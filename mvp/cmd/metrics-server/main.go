// mk-metrics-server aggregates per-pod CPU metrics from all kubelets.
//
// It polls each registered node's kubelet /metrics/pods endpoint, caches
// the results, and exposes them at /apis/metrics.mk.io/v1/pods. The HPA
// controller consumes this endpoint.
//
// Kubelets run on a loopback port; we read the node registration's
// Status.Address to locate each kubelet. For M5 the demo runs a single
// kubelet whose address defaults to 127.0.0.1:<kubelet-metrics-port>.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

type metricsServer struct {
	api      *client.Client
	kubelets []string
	mu       sync.RWMutex
	cache    []api.PodMetric
}

func main() {
	addr := flag.String("addr", ":9100", "listen address")
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	kubeletList := flag.String("kubelets", "http://127.0.0.1:10250", "comma-separated list of kubelet metrics endpoints")
	interval := flag.Duration("interval", 2*time.Second, "scrape interval")
	flag.Parse()

	kls := strings.Split(*kubeletList, ",")
	m := &metricsServer{
		api:      client.New(*apiAddr),
		kubelets: kls,
	}

	go m.scrapeLoop(*interval)

	mux := http.NewServeMux()
	mux.HandleFunc("/apis/metrics.mk.io/v1/pods", m.handlePodMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	log.Printf("mk-metrics-server listening on %s (kubelets=%v)", *addr, kls)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func (m *metricsServer) scrapeLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		m.scrapeOnce()
	}
}

func (m *metricsServer) scrapeOnce() {
	var all []api.PodMetric
	for _, k := range m.kubelets {
		r, err := http.Get(strings.TrimRight(k, "/") + "/metrics/pods")
		if err != nil {
			log.Printf("scrape %s: %v", k, err)
			continue
		}
		func() {
			defer r.Body.Close()
			if r.StatusCode != 200 {
				b, _ := io.ReadAll(r.Body)
				log.Printf("scrape %s: %s: %s", k, r.Status, string(b))
				return
			}
			var got struct {
				Items []api.PodMetric `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				log.Printf("scrape %s decode: %v", k, err)
				return
			}
			all = append(all, got.Items...)
		}()
	}
	m.mu.Lock()
	m.cache = all
	m.mu.Unlock()
}

func (m *metricsServer) handlePodMetrics(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	items := m.cache
	m.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
}
