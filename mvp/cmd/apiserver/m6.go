package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// registerM6Routes adds NetworkPolicy, Cluster, and FederatedDeployment
// endpoints to the given mux.
func (s *server) registerM6Routes(mux *http.ServeMux) {
	mux.HandleFunc("/apis/networking.mk.io/v1/networkpolicies", s.guard("networking.mk.io", "networkpolicies", s.handleNetPols))
	mux.HandleFunc("/apis/networking.mk.io/v1/networkpolicies/", s.guard("networking.mk.io", "networkpolicies", s.handleNetPolsByName))

	mux.HandleFunc("/apis/federation.mk.io/v1/clusters", s.guard("federation.mk.io", "clusters", s.handleClusters))
	mux.HandleFunc("/apis/federation.mk.io/v1/clusters/", s.guard("federation.mk.io", "clusters", s.handleClustersByName))

	mux.HandleFunc("/apis/federation.mk.io/v1/federateddeployments", s.guard("federation.mk.io", "federateddeployments", s.handleFedDeployments))
	mux.HandleFunc("/apis/federation.mk.io/v1/federateddeployments/", s.guard("federation.mk.io", "federateddeployments", s.handleFedDeploymentsByName))
}

// ---------- NetworkPolicy ----------

func (s *server) handleNetPols(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		nps, err := s.st.ListNetworkPolicies()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": nps})
	case http.MethodPost:
		var np api.NetworkPolicy
		if err := json.NewDecoder(r.Body).Decode(&np); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if np.Metadata.Name == "" {
			http.Error(w, "metadata.name required", http.StatusBadRequest)
			return
		}
		if np.Spec.PodSelector == nil {
			np.Spec.PodSelector = map[string]string{}
		}
		np.APIVersion = "networking.mk.io/v1"
		np.Kind = "NetworkPolicy"
		if err := s.st.UpsertNetworkPolicy(&np); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetNetworkPolicy(np.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleNetPolsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/networking.mk.io/v1/networkpolicies/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		np, err := s.st.GetNetworkPolicy(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, np)
	case http.MethodDelete:
		if err := s.st.DeleteNetworkPolicy(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- Clusters ----------

func (s *server) handleClusters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cs, err := s.st.ListClusters()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": cs})
	case http.MethodPost:
		var c api.Cluster
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if c.Metadata.Name == "" || c.Spec.Server == "" {
			http.Error(w, "metadata.name and spec.server required", http.StatusBadRequest)
			return
		}
		c.APIVersion = "federation.mk.io/v1"
		c.Kind = "Cluster"
		if err := s.st.UpsertCluster(&c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetCluster(c.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleClustersByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/federation.mk.io/v1/clusters/")
	if strings.HasSuffix(name, "/status") {
		n := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			var st api.ClusterStatus
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.st.UpdateClusterStatus(n, st); err != nil {
				notFoundOrErr(w, err)
				return
			}
			out, _ := s.st.GetCluster(n)
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
		c, err := s.st.GetCluster(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	case http.MethodDelete:
		if err := s.st.DeleteCluster(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- FederatedDeployment ----------

func (s *server) handleFedDeployments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		fds, err := s.st.ListFederatedDeployments()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": fds})
	case http.MethodPost:
		var fd api.FederatedDeployment
		if err := json.NewDecoder(r.Body).Decode(&fd); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if fd.Metadata.Name == "" {
			http.Error(w, "metadata.name required", http.StatusBadRequest)
			return
		}
		if len(fd.Spec.Template.Template.Spec.Containers) == 0 {
			http.Error(w, "spec.template.template.spec.containers required", http.StatusBadRequest)
			return
		}
		fd.APIVersion = "federation.mk.io/v1"
		fd.Kind = "FederatedDeployment"
		if err := s.st.UpsertFederatedDeployment(&fd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := s.st.GetFederatedDeployment(fd.Metadata.Name)
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleFedDeploymentsByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/apis/federation.mk.io/v1/federateddeployments/")
	if strings.HasSuffix(name, "/status") {
		n := strings.TrimSuffix(name, "/status")
		if r.Method == http.MethodPut || r.Method == http.MethodPatch {
			var st api.FederatedDeploymentStatus
			if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.st.UpdateFederatedDeploymentStatus(n, st); err != nil {
				notFoundOrErr(w, err)
				return
			}
			out, _ := s.st.GetFederatedDeployment(n)
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
		fd, err := s.st.GetFederatedDeployment(name)
		if err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fd)
	case http.MethodDelete:
		if err := s.st.DeleteFederatedDeployment(name); err != nil {
			notFoundOrErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
