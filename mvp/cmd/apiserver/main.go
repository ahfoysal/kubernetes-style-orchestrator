package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/admission"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/controllers"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/crd"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/federation"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/hpa"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/rbac"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/store"
)

// clusterIPAlloc hands out ClusterIPs for Services.
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

var _ = fmt.Sprintf

// server bundles apiserver state so handlers can share it.
type server struct {
	st        *store.Store
	alloc     *clusterIPAlloc
	auth      *rbac.Authenticator
	authz     *rbac.Authorizer
	admission *admission.Pipeline
	// rbacEnforce toggles whether denials actually block requests. When
	// false (default), RBAC decisions are logged but not enforced — lets
	// the M1–M3 demos keep running without tokens.
	rbacEnforce bool
	// identity is this apiserver replica's identity (for lease election).
	identity string
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "data/mk.db", "sqlite db path")
	loopCIDR := flag.Bool("cluster-ip-loopback-cidr", false, "allocate cluster IPs out of 127.20.0.0/24 instead of 127.0.0.1 (requires lo aliases)")
	rbacEnforce := flag.Bool("rbac", false, "enforce RBAC (deny requests that lack permissions)")
	// M5: HA. When two apiservers share the same DB, the one that holds
	// the controller-manager lease runs the RS/Deployment/Service/HPA
	// reconcilers; the other is hot-standby.
	runControllers := flag.Bool("run-controllers", true, "contend for the controller-manager lease and run reconcilers when leader")
	leaseName := flag.String("lease-name", "controller-manager", "lease key to contend for leadership")
	leaseTTL := flag.Duration("lease-ttl", 10*time.Second, "lease TTL (must be > renew interval)")
	leaseRenew := flag.Duration("lease-renew", 3*time.Second, "lease renewal interval")
	identity := flag.String("identity", "", "this apiserver's unique identity (default: hostname+addr)")
	metricsURL := flag.String("metrics-url", "http://127.0.0.1:9100", "metrics-server base URL (for HPA)")
	apiSelf := flag.String("api-self", "", "this apiserver's reachable URL used by in-process controllers (default: http://127.0.0.1<addr>)")
	flag.Parse()

	if *identity == "" {
		host, _ := os.Hostname()
		*identity = host + *addr
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	s := &server{
		st:          st,
		alloc:       &clusterIPAlloc{next: 1, useLoop: *loopCIDR},
		auth:        &rbac.Authenticator{Store: st, AnonymousAllowed: true},
		authz:       &rbac.Authorizer{Store: st},
		admission:   admission.New(st),
		rbacEnforce: *rbacEnforce,
		identity:    *identity,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Core API
	mux.HandleFunc("/api/v1/pods", s.guard("", "pods", s.handlePods))
	mux.HandleFunc("/api/v1/pods/", s.guard("", "pods", s.handlePodsByName))
	mux.HandleFunc("/api/v1/services", s.guard("", "services", s.handleServices))
	mux.HandleFunc("/api/v1/services/", s.guard("", "services", s.handleServicesByName))
	mux.HandleFunc("/api/v1/endpoints", s.guard("", "endpoints", s.handleEndpoints))
	mux.HandleFunc("/api/v1/endpoints/", s.guard("", "endpoints", s.handleEndpointsByName))
	mux.HandleFunc("/api/v1/nodes", s.guard("", "nodes", s.handleNodes))
	mux.HandleFunc("/api/v1/nodes/", s.guard("", "nodes", s.handleNodesByName))

	// apps/v1
	mux.HandleFunc("/apis/apps/v1/replicasets", s.guard("apps", "replicasets", s.handleRS))
	mux.HandleFunc("/apis/apps/v1/replicasets/", s.guard("apps", "replicasets", s.handleRSByName))
	mux.HandleFunc("/apis/apps/v1/deployments", s.guard("apps", "deployments", s.handleDeployments))
	mux.HandleFunc("/apis/apps/v1/deployments/", s.guard("apps", "deployments", s.handleDeploymentsByName))

	// CRDs themselves live in mk.io/v1 so the "apply a CRD" REST call is
	// symmetric with every other resource.
	mux.HandleFunc("/apis/mk.io/v1/customresourcedefinitions", s.guard("mk.io", "customresourcedefinitions", s.handleCRDs))
	mux.HandleFunc("/apis/mk.io/v1/customresourcedefinitions/", s.guard("mk.io", "customresourcedefinitions", s.handleCRDsByName))

	// RBAC resources.
	mux.HandleFunc("/apis/rbac.mk.io/v1/users", s.guard("rbac.mk.io", "users", s.handleUsers))
	mux.HandleFunc("/apis/rbac.mk.io/v1/roles", s.guard("rbac.mk.io", "roles", s.handleRoles("Role")))
	mux.HandleFunc("/apis/rbac.mk.io/v1/clusterroles", s.guard("rbac.mk.io", "clusterroles", s.handleRoles("ClusterRole")))
	mux.HandleFunc("/apis/rbac.mk.io/v1/rolebindings", s.guard("rbac.mk.io", "rolebindings", s.handleRoleBindings("RoleBinding")))
	mux.HandleFunc("/apis/rbac.mk.io/v1/clusterrolebindings", s.guard("rbac.mk.io", "clusterrolebindings", s.handleRoleBindings("ClusterRoleBinding")))

	// M5: Admission webhooks registry.
	mux.HandleFunc("/apis/admission.mk.io/v1/webhooks", s.guard("admission.mk.io", "webhooks", s.handleWebhooks))
	mux.HandleFunc("/apis/admission.mk.io/v1/webhooks/", s.guard("admission.mk.io", "webhooks", s.handleWebhooksByName))

	// M5: HPA.
	mux.HandleFunc("/apis/autoscaling.mk.io/v1/horizontalpodautoscalers", s.guard("autoscaling.mk.io", "horizontalpodautoscalers", s.handleHPAs))
	mux.HandleFunc("/apis/autoscaling.mk.io/v1/horizontalpodautoscalers/", s.guard("autoscaling.mk.io", "horizontalpodautoscalers", s.handleHPAsByName))

	// M5: observability — show who holds the controller-manager lease.
	mux.HandleFunc("/apis/coordination.mk.io/v1/leases/", s.handleLease)

	// M6: NetworkPolicy + Cluster + FederatedDeployment routes.
	s.registerM6Routes(mux)

	// Catch-all for dynamic CR paths: /apis/{group}/{version}/{plural}[/{name}]
	// Routes that matched a static handler above won't reach this.
	mux.HandleFunc("/apis/", s.handleDynamicCR)

	// Start leader election + in-process controllers if requested.
	if *runControllers {
		if *apiSelf == "" {
			*apiSelf = "http://127.0.0.1" + *addr
		}
		go s.runLeaderElected(*leaseName, *leaseTTL, *leaseRenew, *apiSelf, *metricsURL)
	}

	log.Printf("mk-apiserver listening on %s (db=%s rbac=%v identity=%s)", *addr, *dbPath, *rbacEnforce, *identity)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// runLeaderElected contends for the given lease; while leader, runs the
// ReplicaSet/Deployment/Service/HPA controllers in-process against
// apiSelf. Loses leadership => stops controllers; regains => restarts.
func (s *server) runLeaderElected(leaseName string, ttl, renew time.Duration, apiSelf, metricsURL string) {
	var (
		leading    bool
		cancelCtrl context.CancelFunc
	)
	ticker := time.NewTicker(renew)
	defer ticker.Stop()
	for range ticker.C {
		ok, holder, err := s.st.TryAcquireLease(leaseName, s.identity, ttl)
		if err != nil {
			log.Printf("lease: acquire error: %v", err)
			continue
		}
		if ok && !leading {
			log.Printf("lease: %s acquired by %s — starting controllers", leaseName, s.identity)
			leading = true
			ctx, cancel := context.WithCancel(context.Background())
			cancelCtrl = cancel
			go s.startControllers(ctx, apiSelf, metricsURL)
		} else if !ok && leading {
			log.Printf("lease: lost to %s — stopping controllers", holder)
			leading = false
			if cancelCtrl != nil {
				cancelCtrl()
			}
		} else if !ok {
			log.Printf("lease: standby (leader=%s)", holder)
		}
	}
}

func (s *server) startControllers(ctx context.Context, apiSelf, metricsURL string) {
	c := client.New(apiSelf)
	rc := controllers.NewReplicaSetController(c, 1*time.Second)
	dc := controllers.NewDeploymentController(c, 1*time.Second)
	sc := controllers.NewServiceController(c, 1*time.Second)
	hc := hpa.New(c, metricsURL, 2*time.Second)

	fc := federation.New(c, 3*time.Second)

	stop := make(chan struct{})
	go rc.Run(stop)
	go dc.Run(stop)
	go sc.Run(stop)
	go hc.Run(stop)
	go fc.Run(stop)

	<-ctx.Done()
	close(stop)
}

// handleLease returns the current holder of a lease (observability).
func (s *server) handleLease(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/coordination.mk.io/v1/leases/")
	if name == "" {
		http.Error(w, "lease name required", http.StatusBadRequest)
		return
	}
	holder, renew, ttl, err := s.st.GetLease(name)
	if err != nil {
		notFoundOrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":          name,
		"holder":        holder,
		"renewTime":     renew,
		"ttlSeconds":    ttl,
		"expiresIn":     time.Duration(ttl)*time.Second - time.Since(renew),
		"thisReplica":   s.identity,
		"isThisReplica": holder == s.identity,
	})
}

// ---------- M5: Admission Webhooks ----------

func (s *server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		hs, err := s.st.ListWebhooks()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": hs})
	case http.MethodPost:
		var h api.AdmissionWebhook
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if h.Metadata.Name == "" || h.URL == "" || h.Type == "" {
			http.Error(w, "metadata.name, url, type required", http.StatusBadRequest)
			return
		}
		if h.Type != api.WebhookTypeMutating && h.Type != api.WebhookTypeValidating {
			http.Error(w, "type must be Mutating or Validating", http.StatusBadRequest)
			return
		}
		h.APIVersion = "admission.mk.io/v1"
		h.Kind = "AdmissionWebhook"
		if err := s.st.UpsertWebhook(&h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("admission: registered webhook name=%s type=%s resources=%v url=%s", h.Metadata.Name, h.Type, h.Resources, h.URL)
		writeJSON(w, http.StatusCreated, h)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleWebhooksByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/admission.mk.io/v1/webhooks/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.st.DeleteWebhook(name); err != nil {
		notFoundOrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// ---------- M5: HPA ----------

func (s *server) handleHPAs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		hs, err := s.st.ListHPAs()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": hs})
	case http.MethodPost:
		var h api.HPA
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if h.Metadata.Name == "" || h.Spec.ScaleTargetRef.Name == "" {
			http.Error(w, "metadata.name and spec.scaleTargetRef.name required", http.StatusBadRequest)
			return
		}
		if h.Spec.MinReplicas <= 0 {
			h.Spec.MinReplicas = 1
		}
		if h.Spec.MaxReplicas < h.Spec.MinReplicas {
			http.Error(w, "maxReplicas must be >= minReplicas", http.StatusBadRequest)
			return
		}
		h.APIVersion = "autoscaling.mk.io/v1"
		h.Kind = "HorizontalPodAutoscaler"
		if err := s.st.UpsertHPA(&h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetHPA(h.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleHPAsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers/")
	if strings.HasSuffix(name, "/status") {
		n := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			var st api.HPAStatus
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.st.UpdateHPAStatus(n, st); err != nil {
				notFoundOrErr(w, err)
				return
			}
			out, _ := s.st.GetHPA(n)
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
		h, err := s.st.GetHPA(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	case http.MethodDelete:
		if err := s.st.DeleteHPA(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// guard wraps a handler with authentication + RBAC authorization.
func (s *server) guard(apiGroup, resource string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.auth.Authenticate(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if s.rbacEnforce {
			verb := verbFromMethod(r.Method)
			err := s.authz.Authorize(rbac.Request{
				User: user, Verb: verb, APIGroup: apiGroup,
				Resource: resource, Namespace: r.URL.Query().Get("namespace"),
			})
			if err != nil {
				if rbac.IsDenied(err) {
					http.Error(w, err.Error(), http.StatusForbidden)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		h(w, r)
	}
}

func verbFromMethod(m string) string {
	switch m {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	}
	return strings.ToLower(m)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------- Pods ----------

func (s *server) handlePods(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listPods(w, r, s.st)
	case http.MethodPost:
		s.createPod(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handlePodsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/pods/")
	if strings.HasSuffix(name, "/status") {
		podName := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			updateStatus(w, r, s.st, podName)
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
		getPod(w, r, s.st, name)
	case http.MethodDelete:
		deletePod(w, r, s.st, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- Services ----------

func (s *server) handleServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		svcs, err := s.st.ListServices()
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
		if existing, err := s.st.GetService(svc.Metadata.Name); err == nil {
			svc.Spec.ClusterIP = existing.Spec.ClusterIP
		} else if svc.Spec.ClusterIP == "" {
			taken, _ := s.st.ListClusterIPs()
			svc.Spec.ClusterIP = s.alloc.alloc(taken)
			if svc.Spec.ClusterIP == "" {
				http.Error(w, "cluster IP pool exhausted", http.StatusInternalServerError)
				return
			}
		}
		if err := s.st.UpsertService(&svc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetService(svc.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleServicesByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/services/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		svc, err := s.st.GetService(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, svc)
	case http.MethodDelete:
		if err := s.st.DeleteService(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- Endpoints ----------

func (s *server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		eps, err := s.st.ListEndpoints()
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
		if err := s.st.UpsertEndpoints(&ep); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetEndpoints(ep.Metadata.Name)
		writeJSON(w, http.StatusOK, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleEndpointsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/endpoints/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ep, err := s.st.GetEndpoints(name)
	if err != nil {
		notFoundOrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ep)
}

// ---------- Nodes (M4) ----------

func (s *server) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ns, err := s.st.ListNodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": ns})
	case http.MethodPost, http.MethodPut:
		var n api.Node
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if n.Metadata.Name == "" {
			http.Error(w, "metadata.name required", http.StatusBadRequest)
			return
		}
		n.APIVersion = "v1"
		n.Kind = "Node"
		n.Status.Ready = true
		if err := s.st.UpsertNode(&n); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetNode(n.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleNodesByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		n, err := s.st.GetNode(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	case http.MethodDelete:
		if err := s.st.DeleteNode(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- apps/v1 built-ins (unchanged) ----------

func (s *server) handleRS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rss, err := s.st.ListReplicaSets()
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
		if err := s.st.UpsertReplicaSet(&rs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetReplicaSet(rs.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleRSByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/apps/v1/replicasets/")
	if strings.HasSuffix(name, "/status") {
		n := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			var st api.ReplicaSetStatus
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.st.UpdateReplicaSetStatus(n, st); err != nil {
				notFoundOrErr(w, err)
				return
			}
			out, _ := s.st.GetReplicaSet(n)
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
		rs, err := s.st.GetReplicaSet(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rs)
	case http.MethodDelete:
		if err := s.st.DeleteReplicaSet(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ds, err := s.st.ListDeployments()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": ds})
	case http.MethodPost:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var d api.Deployment
		if err := json.Unmarshal(raw, &d); err != nil {
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
		// Admission: let webhooks mutate/validate the deployment.
		norm, _ := json.Marshal(d)
		mutated, aerr := s.admission.Review("deployments", "Deployment", "CREATE", d.Metadata.Name, d.Metadata.Namespace, norm)
		if aerr != nil {
			http.Error(w, aerr.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(mutated, &d); err != nil {
			http.Error(w, "admission produced invalid object: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.st.UpsertDeployment(&d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetDeployment(d.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleDeploymentsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/apps/v1/deployments/")
	if strings.HasSuffix(name, "/status") {
		n := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			var st api.DeploymentStatus
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.st.UpdateDeploymentStatus(n, st); err != nil {
				notFoundOrErr(w, err)
				return
			}
			out, _ := s.st.GetDeployment(n)
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
		d, err := s.st.GetDeployment(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	case http.MethodDelete:
		if err := s.st.DeleteDeployment(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- CRDs (M4) ----------

func (s *server) handleCRDs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cs, err := s.st.ListCRDs()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": cs})
	case http.MethodPost:
		var c api.CRD
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if c.Metadata.Name == "" {
			http.Error(w, "metadata.name required", http.StatusBadRequest)
			return
		}
		if c.Spec.Group == "" || c.Spec.Version == "" || c.Spec.Names.Plural == "" || c.Spec.Names.Kind == "" {
			http.Error(w, "spec.group, spec.version, spec.names.plural, spec.names.kind required", http.StatusBadRequest)
			return
		}
		c.APIVersion = "mk.io/v1"
		c.Kind = "CustomResourceDefinition"
		if c.Spec.Scope == "" {
			c.Spec.Scope = "Namespaced"
		}
		if err := s.st.UpsertCRD(&c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetCRD(c.Metadata.Name)
		log.Printf("crd: registered %s (%s/%s %s)", c.Metadata.Name, c.Spec.Group, c.Spec.Version, c.Spec.Names.Plural)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleCRDsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/mk.io/v1/customresourcedefinitions/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c, err := s.st.GetCRD(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	case http.MethodDelete:
		if err := s.st.DeleteCRD(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDynamicCR resolves any /apis/{group}/{version}/{plural}[/{name}] URL
// to a registered CRD and handles CR create/list/get/delete in a single
// generic codepath.
func (s *server) handleDynamicCR(w http.ResponseWriter, r *http.Request) {
	group, version, plural, name, err := crd.ParsePath(r.URL.Path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Look up CRD by plural and check group/version match.
	c, err := s.st.GetCRDByPlural(plural)
	if err != nil {
		http.Error(w, "no CRD registered for plural="+plural, http.StatusNotFound)
		return
	}
	if c.Spec.Group != group || c.Spec.Version != version {
		http.Error(w, fmt.Sprintf("CRD %s registered at %s/%s, not %s/%s", c.Metadata.Name, c.Spec.Group, c.Spec.Version, group, version), http.StatusNotFound)
		return
	}

	// Authenticate + authorize using the CRD's apiGroup + plural as resource.
	user, ok := s.auth.Authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.rbacEnforce {
		err := s.authz.Authorize(rbac.Request{
			User: user, Verb: verbFromMethod(r.Method),
			APIGroup: group, Resource: plural,
			Namespace: r.URL.Query().Get("namespace"),
		})
		if err != nil {
			if rbac.IsDenied(err) {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	crdName := c.Metadata.Name
	switch {
	case name == "":
		switch r.Method {
		case http.MethodGet:
			crs, err := s.st.ListCustomResources(crdName)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": crs})
		case http.MethodPost:
			var cr api.CustomResource
			if err := json.NewDecoder(r.Body).Decode(&cr); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if cr.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			if err := crd.ValidateSpec(c.Spec.Schema, cr.Spec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			cr.APIVersion = group + "/" + version
			cr.Kind = c.Spec.Names.Kind
			if err := s.st.UpsertCustomResource(crdName, &cr); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out, _ := s.st.GetCustomResource(crdName, cr.Metadata.Name)
			writeJSON(w, http.StatusCreated, out)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		switch r.Method {
		case http.MethodGet:
			cr, err := s.st.GetCustomResource(crdName, name)
			if err != nil {
				notFoundOrErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, cr)
		case http.MethodDelete:
			if err := s.st.DeleteCustomResource(crdName, name); err != nil {
				notFoundOrErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ---------- RBAC resources ----------

func (s *server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		us, err := s.st.ListUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": us})
	case http.MethodPost:
		var u api.User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if u.Metadata.Name == "" || u.Token == "" {
			http.Error(w, "metadata.name + token required", http.StatusBadRequest)
			return
		}
		u.APIVersion = "rbac.mk.io/v1"
		u.Kind = "User"
		if err := s.st.UpsertUser(&u); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, u)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleRoles(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			rs, err := s.st.ListRoles(scope)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": rs})
		case http.MethodPost:
			var role api.Role
			if err := json.NewDecoder(r.Body).Decode(&role); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if role.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			role.APIVersion = "rbac.mk.io/v1"
			role.Kind = scope
			if err := s.st.UpsertRole(scope, &role); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusCreated, role)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (s *server) handleRoleBindings(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			bs, err := s.st.ListRoleBindings(scope)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": bs})
		case http.MethodPost:
			var b api.RoleBinding
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if b.Metadata.Name == "" {
				http.Error(w, "metadata.name required", http.StatusBadRequest)
				return
			}
			b.APIVersion = "rbac.mk.io/v1"
			b.Kind = scope
			if err := s.st.UpsertRoleBinding(scope, &b); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusCreated, b)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ---------- helpers ----------

func notFoundOrErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
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

func (s *server) createPod(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var p api.Pod
	if err := json.Unmarshal(raw, &p); err != nil {
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
	// Admission: serialize + let the pipeline mutate/validate.
	norm, _ := json.Marshal(p)
	mutated, aerr := s.admission.Review("pods", "Pod", "CREATE", p.Metadata.Name, p.Metadata.Namespace, norm)
	if aerr != nil {
		http.Error(w, aerr.Error(), http.StatusBadRequest)
		return
	}
	var finalPod api.Pod
	if err := json.Unmarshal(mutated, &finalPod); err != nil {
		http.Error(w, "admission produced invalid object: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.st.UpsertPod(&finalPod); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out, _ := s.st.GetPod(finalPod.Metadata.Name)
	writeJSON(w, http.StatusCreated, out)
}

func getPod(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	p, err := st.GetPod(name)
	if err != nil {
		notFoundOrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func deletePod(w http.ResponseWriter, r *http.Request, st *store.Store, name string) {
	if err := st.DeletePod(name); err != nil {
		notFoundOrErr(w, err)
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
		notFoundOrErr(w, err)
		return
	}
	p, _ := st.GetPod(name)
	writeJSON(w, http.StatusOK, p)
}
