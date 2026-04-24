// mk-sidecar-injector is a mutating admission webhook that injects a
// `mk-sidecar` container alongside any Pod carrying the opt-in label
// `mk.io/inject-sidecar=true`. It mirrors istio/linkerd's sidecar
// injection pattern.
//
// Register against the apiserver with:
//
//	mkctl apply -f examples/webhook-sidecar-inject.yaml
//
// The injected container is informational in the MVP — the full mTLS
// data-plane lives in `mk-sidecar` and is wired up out-of-band by the
// M6 demo script. The injection itself proves the webhook chain can
// materialise sidecars automatically.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

const injectLabel = "mk.io/inject-sidecar"
const sidecarImage = "mk-sidecar:dev"

func main() {
	addr := flag.String("addr", ":9444", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/inject-sidecar", injectSidecar)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	log.Printf("mk-sidecar-injector listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func injectSidecar(w http.ResponseWriter, r *http.Request) {
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
	// Opt-in gate.
	if p.Metadata.Labels[injectLabel] != "true" {
		writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: true})
		return
	}
	// Idempotent: if already injected, do nothing.
	for _, c := range p.Spec.Containers {
		if c.Name == "mk-sidecar" {
			writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: true})
			return
		}
	}
	// Append the sidecar container.
	p.Spec.Containers = append(p.Spec.Containers, api.Container{
		Name:  "mk-sidecar",
		Image: sidecarImage,
		Args:  []string{"-mode=server", "-listen=:9443", "-upstream=127.0.0.1:8080"},
		Ports: []api.ContainerPort{{
			Name: "mtls", ContainerPort: 9443, Protocol: "TCP",
		}},
	})
	if p.Metadata.Labels == nil {
		p.Metadata.Labels = map[string]string{}
	}
	p.Metadata.Labels["mk.io/sidecar-injected"] = "true"

	patch, _ := json.Marshal(p)
	log.Printf("sidecar-inject: pod=%s injected mk-sidecar container", p.Metadata.Name)
	writeResp(w, api.AdmissionReviewResponse{UID: req.UID, Allowed: true, Patch: patch})
}

func writeResp(w http.ResponseWriter, resp api.AdmissionReviewResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
