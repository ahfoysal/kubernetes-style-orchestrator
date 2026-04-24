// mk-sample-webhook runs two demo admission webhooks side-by-side:
//
//	/mutate-add-label   — mutating: injects metadata.labels.mk-admitted=true
//	/validate-memlimit  — validating: rejects Pods with no memory limit
//
// Register them with the apiserver via `mkctl apply -f webhook-*.yaml`.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

func main() {
	addr := flag.String("addr", ":9443", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-add-label", mutateAddLabel)
	mux.HandleFunc("/validate-memlimit", validateMemLimit)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	log.Printf("mk-sample-webhook listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// mutateAddLabel adds a mk-admitted=true label to any resource that passes
// through it. Returns a full-object Patch.
func mutateAddLabel(w http.ResponseWriter, r *http.Request) {
	var req api.AdmissionReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Parse as a generic map so we don't have to know the concrete kind.
	var obj map[string]interface{}
	if err := json.Unmarshal(req.Object, &obj); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	meta, _ := obj["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
		obj["metadata"] = meta
	}
	labels, _ := meta["labels"].(map[string]interface{})
	if labels == nil {
		labels = map[string]interface{}{}
		meta["labels"] = labels
	}
	labels["mk-admitted"] = "true"
	labels["mk-admitted-at"] = "webhook"

	patch, _ := json.Marshal(obj)
	log.Printf("mutate: %s/%s labeled mk-admitted=true", req.Resource, req.Name)
	writeResp(w, api.AdmissionReviewResponse{
		UID: req.UID, Allowed: true, Patch: patch,
	})
}

// validateMemLimit rejects Pods whose containers lack resources.limits.memory.
// Passes every other resource through.
func validateMemLimit(w http.ResponseWriter, r *http.Request) {
	var req api.AdmissionReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Resource != "pods" {
		writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: true})
		return
	}
	var p api.Pod
	if err := json.Unmarshal(req.Object, &p); err != nil {
		writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: false, Message: "cannot parse Pod: " + err.Error()})
		return
	}
	for _, c := range p.Spec.Containers {
		if c.Resources.Limits == nil || c.Resources.Limits["memory"] == "" {
			msg := "container " + c.Name + " missing resources.limits.memory"
			log.Printf("validate: DENY pod=%s reason=%s", p.Metadata.Name, msg)
			writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: false, Message: msg})
			return
		}
	}
	log.Printf("validate: allow pod=%s (memory limits present)", p.Metadata.Name)
	writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: true})
}

func writeResp(w http.ResponseWriter, resp api.AdmissionReviewResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
