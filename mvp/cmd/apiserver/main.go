package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/store"
)

// clusterIPAlloc hands out ClusterIPs for Services.
//
// On a real multi-node cluster each Service gets a unique /32 out of a
// reserved CIDR, and kube-proxy installs iptables/IPVS rules on every
// node. In our single-host MVP we bind the L4 proxy on the loopback
// interface. We use 127.20.0.X if the operator has aliased those onto
// lo (see README); otherwise the universally-available 127.0.0.1 is
// used and each Service must pick a unique spec.ports[].port.
type clusterIPAlloc struct {
	mu      sync.Mutex
	next    byte
	useLoop bool
}

func (a *clusterIPAlloc) alloc(taken map[string]bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.useLoop {
		return "127.0.0.1"
	}
	for i := 0; i < 250; i++ {
		a.next++
		if a.next < 2 {
			a.next = 2
		}
		ip := fmt.Sprintf("127.20.0.%d", a.next)
		if !taken[ip] {
			return ip
		}
	}
	return ""
}

var _ = fmt.Sprintf // keep fmt imported when useLoop path is unused

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "data/mk.db", "sqlite db path")
	loopCIDR := flag.Bool("cluster-ip-loopback-cidr", false, "allocate cluster IPs out of 127.20.0.0/24 instead of 127.0.0.1 (requires lo aliases)")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	alloc := &clusterIPAlloc{next: 1, useLoop: *loopCIDR}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/v1/services", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			svcs, err := st.ListServices()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": svcs})
		case http.MethodPost:
			var svc api.Service
			if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if svc.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			if len(svc.Spec.Ports) == 0 {
				http.Error(w, "spec.ports required", http.StatusBadRequest)
				return
			}
			if svc.Spec.Type == "" {
				svc.Spec.Type = api.ServiceTypeClusterIP
			}
			svc.APIVersion = "v1"
			svc.Kind = "Service"
			// Allocate ClusterIP on first create.
			if existing, err := st.GetService(svc.Metadata.Name); err == nil {
				svc.Spec.ClusterIP = existing.Spec.ClusterIP
			} else if svc.Spec.ClusterIP == "" {
				taken, _ := st.ListClusterIPs()
				svc.Spec.ClusterIP = alloc.alloc(taken)
				if svc.Spec.ClusterIP == "" {
					http.Error(w, "cluster IP pool exhausted", http.StatusInternalServerError)
					return
				}
			}
			if err := st.UpsertService(&svc); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out, _ := st.GetService(svc.Metadata.Name)
			writeJSON(w, http.StatusCreated, out)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/services/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/services/")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			svc, err := st.GetService(name)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, svc)
		case http.MethodDelete:
			if err := st.DeleteService(name); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/endpoints", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			eps, err := st.ListEndpoints()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": eps})
		case http.MethodPost, http.MethodPut:
			var ep api.Endpoints
			if err := json.NewDecoder(r.Body).Decode(&ep); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if ep.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			ep.APIVersion = "v1"
			ep.Kind = "Endpoints"
			if err := st.UpsertEndpoints(&ep); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out, _ := st.GetEndpoints(ep.Metadata.Name)
			writeJSON(w, http.StatusOK, out)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/endpoints/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/endpoints/")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ep, err := st.GetEndpoints(name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, ep)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listPods(w, r, st)
		case http.MethodPost:
			createPod(w, r, st)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/apis/apps/v1/replicasets", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			rss, err := st.ListReplicaSets()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": rss})
		case http.MethodPost:
			var rs api.ReplicaSet
			if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if rs.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			rs.APIVersion = "apps/v1"
			rs.Kind = "ReplicaSet"
			if err := st.UpsertReplicaSet(&rs); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out, _ := st.GetReplicaSet(rs.Metadata.Name)
			writeJSON(w, http.StatusCreated, out)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/apis/apps/v1/replicasets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/apis/apps/v1/replicasets/")
		if strings.HasSuffix(name, "/status") {
			n := strings.TrimSuffix(name, "/status")
			if r.Method == http.MethodPut || r.Method == http.MethodPatch {
				var s api.ReplicaSetStatus
				if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
					http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := st.UpdateReplicaSetStatus(n, s); err != nil {
					if errors.Is(err, store.ErrNotFound) {
						http.Error(w, "not found", http.StatusNotFound)
						return
					}
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				out, _ := st.GetReplicaSet(n)
				writeJSON(w, http.StatusOK, out)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			rs, err := st.GetReplicaSet(name)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, rs)
		case http.MethodDelete:
			if err := st.DeleteReplicaSet(name); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ds, err := st.ListDeployments()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": ds})
		case http.MethodPost:
			var d api.Deployment
			if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if d.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			if d.Spec.Replicas < 0 {
				http.Error(w, "spec.replicas must be >= 0", http.StatusBadRequest)
				return
			}
			if len(d.Spec.Template.Spec.Containers) == 0 {
				http.Error(w, "spec.template.spec.containers required", http.StatusBadRequest)
				return
			}
			d.APIVersion = "apps/v1"
			d.Kind = "Deployment"
			if err := st.UpsertDeployment(&d); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out, _ := st.GetDeployment(d.Metadata.Name)
			writeJSON(w, http.StatusCreated, out)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/apis/apps/v1/deployments/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/apis/apps/v1/deployments/")
		if strings.HasSuffix(name, "/status") {
			n := strings.TrimSuffix(name, "/status")
			if r.Method == http.MethodPut || r.Method == http.MethodPatch {
				var s api.DeploymentStatus
				if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
					http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := st.UpdateDeploymentStatus(n, s); err != nil {
					if errors.Is(err, store.ErrNotFound) {
						http.Error(w, "not found", http.StatusNotFound)
						return
					}
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				out, _ := st.GetDeployment(n)
				writeJSON(w, http.StatusOK, out)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			d, err := st.GetDeployment(name)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, d)
		case http.MethodDelete:
			if err := st.DeleteDeployment(name); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/pods/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/pods/")
		// Allow /api/v1/pods/{name}/status (PUT/PATCH)
		if strings.HasSuffix(name, "/status") {
			podName := strings.TrimSuffix(name, "/status")
			if r.Method == http.MethodPut || r.Method == http.MethodPatch {
				updateStatus(w, r, st, podName)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if name == "" {
			http.Error(w, "pod name required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			getPod(w, r, st, name)
		case http.MethodDelete:
			deletePod(w, r, st, name)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	log.Printf("mk-apiserver listening on %s (db=%s)", *addr, *dbPath)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func listPods(w http.ResponseWriter, r *http.Request, st *store.Store) {
	phase := r.URL.Query().Get("phase")
	node := r.URL.Query().Get("nodeName")
	var (
		pods []*api.Pod
		err  error
	)
	if node != "" {
		pods, err = st.ListPodsByNode(node)
	} else {
		pods, err = st.ListPods(phase)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// filter by phase on node-listed results
	if node != "" && phase != "" {
		filtered := pods[:0]
		for _, p := range pods {
			if p.Status.Phase == phase {
				filtered = append(filtered, p)
			}
		}
		pods = filtered
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": pods})
}

func createPod(w http.ResponseWriter, r *http.Request, st *store.Store) {
	var p api.Pod
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Metadata.Name == "" {
		http.Error(w, "metadata.name required", http.StatusBadRequest)
		return
	}
	if len(p.Spec.Containers) == 0 {
		http.Error(w, "spec.containers required", http.StatusBadRequest)
		return
	}
	p.APIVersion = "v1"
	p.Kind = "Pod"
	if err := st.UpsertPod(&p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out, _ := st.GetPod(p.Metadata.Name)
	writeJSON(w, http.StatusCreated, out)
}

func getPod(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	p, err := st.GetPod(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func deletePod(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	if err := st.DeletePod(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func updateStatus(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	var s api.PodStatus
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := st.UpdateStatus(name, s); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, _ := st.GetPod(name)
	writeJSON(w, http.StatusOK, p)
}
