// Package admission implements the webhook admission pipeline used by
// the apiserver before persisting resources. It mirrors Kubernetes'
// MutatingAdmissionWebhook + ValidatingAdmissionWebhook chains.
//
// Flow (per CREATE/UPDATE):
//
//  1. For every registered Mutating webhook whose .resources matches the
//     resource plural, POST an AdmissionReviewRequest. The webhook may
//     return a Patch (full replacement JSON) — the object is rewritten.
//  2. For every registered Validating webhook, POST the (possibly mutated)
//     object. If any returns Allowed=false, the request is rejected with
//     that message (HTTP 400).
//
// FailurePolicy of "Fail" means a transport error from the webhook also
// rejects. "Ignore" treats errors as an implicit allow.
package admission

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/store"
)

// Pipeline runs registered webhooks against incoming resource JSON.
type Pipeline struct {
	Store  *store.Store
	Client *http.Client
}

// New creates a Pipeline bound to the given store.
func New(st *store.Store) *Pipeline {
	return &Pipeline{
		Store:  st,
		Client: &http.Client{Timeout: 3 * time.Second},
	}
}

// Review runs the pipeline. resource is the lowercase plural (pods,
// deployments, ...). Object is the raw JSON of the resource being created/
// updated. Returns the possibly-mutated object bytes and any rejection.
func (p *Pipeline) Review(resource, kind, operation, name, namespace string, object []byte) ([]byte, error) {
	hooks, err := p.Store.ListWebhooks()
	if err != nil {
		return object, fmt.Errorf("admission: list webhooks: %w", err)
	}
	current := object
	// Mutating first.
	for _, h := range hooks {
		if h.Type != api.WebhookTypeMutating || !matchesResource(h.Resources, resource) {
			continue
		}
		mutated, denyMsg, err := p.call(h, resource, kind, operation, name, namespace, current)
		if err != nil {
			if h.FailurePolicy == "Ignore" {
				log.Printf("admission: mutating webhook %s error (ignored): %v", h.Metadata.Name, err)
				continue
			}
			return current, fmt.Errorf("admission: mutating webhook %s: %w", h.Metadata.Name, err)
		}
		if denyMsg != "" {
			return current, fmt.Errorf("admission: denied by mutating webhook %s: %s", h.Metadata.Name, denyMsg)
		}
		if len(mutated) > 0 {
			current = mutated
			log.Printf("admission: mutating webhook %s patched %s/%s", h.Metadata.Name, resource, name)
		}
	}
	// Validating next.
	for _, h := range hooks {
		if h.Type != api.WebhookTypeValidating || !matchesResource(h.Resources, resource) {
			continue
		}
		_, denyMsg, err := p.call(h, resource, kind, operation, name, namespace, current)
		if err != nil {
			if h.FailurePolicy == "Ignore" {
				log.Printf("admission: validating webhook %s error (ignored): %v", h.Metadata.Name, err)
				continue
			}
			return current, fmt.Errorf("admission: validating webhook %s: %w", h.Metadata.Name, err)
		}
		if denyMsg != "" {
			return current, fmt.Errorf("admission: denied by %s: %s", h.Metadata.Name, denyMsg)
		}
	}
	return current, nil
}

func (p *Pipeline) call(h *api.AdmissionWebhook, resource, kind, operation, name, namespace string, obj []byte) (patched []byte, denyMsg string, err error) {
	uid := randID()
	req := api.AdmissionReviewRequest{
		UID:       uid,
		Kind:      kind,
		Resource:  resource,
		Namespace: namespace,
		Name:      name,
		Operation: operation,
		Object:    json.RawMessage(obj),
	}
	body, _ := json.Marshal(req)
	r, err := p.Client.Post(h.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		b, _ := io.ReadAll(r.Body)
		return nil, "", fmt.Errorf("webhook returned %s: %s", r.Status, string(b))
	}
	var resp api.AdmissionReviewResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, "", fmt.Errorf("decode webhook response: %w", err)
	}
	if !resp.Allowed {
		return nil, nz(resp.Message, "denied"), nil
	}
	return resp.Patch, "", nil
}

func matchesResource(list []string, res string) bool {
	for _, x := range list {
		if x == "*" || x == res {
			return true
		}
	}
	return false
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nz(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
