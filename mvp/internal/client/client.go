// Package client is a tiny HTTP client for the mk-apiserver.
// It is used by controllers and CLI tooling to avoid import-cycles
// and repeated boilerplate.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// Client talks to mk-apiserver over REST.
type Client struct {
	Base string
	HTTP *http.Client
	// Token is an optional bearer token sent in Authorization.
	Token string
}

func New(base string) *Client {
	return &Client{Base: base, HTTP: http.DefaultClient}
}

// NewWithToken returns a client that attaches Authorization: Bearer <token>.
func NewWithToken(base, token string) *Client {
	return &Client{Base: base, HTTP: http.DefaultClient, Token: token}
}

func (c *Client) do(method, path string, body interface{}, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, string(b))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---------- Pods ----------

func (c *Client) ListPods() ([]*api.Pod, error) {
	var out struct {
		Items []*api.Pod `json:"items"`
	}
	if err := c.do(http.MethodGet, "/api/v1/pods", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreatePod(p *api.Pod) (*api.Pod, error) {
	var out api.Pod
	if err := c.do(http.MethodPost, "/api/v1/pods", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeletePod(name string) error {
	return c.do(http.MethodDelete, "/api/v1/pods/"+name, nil, nil)
}

// ---------- ReplicaSets ----------

func (c *Client) ListReplicaSets() ([]*api.ReplicaSet, error) {
	var out struct {
		Items []*api.ReplicaSet `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/apps/v1/replicasets", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetReplicaSet(name string) (*api.ReplicaSet, error) {
	var out api.ReplicaSet
	if err := c.do(http.MethodGet, "/apis/apps/v1/replicasets/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateReplicaSet(rs *api.ReplicaSet) (*api.ReplicaSet, error) {
	var out api.ReplicaSet
	if err := c.do(http.MethodPost, "/apis/apps/v1/replicasets", rs, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateReplicaSetStatus(name string, s api.ReplicaSetStatus) error {
	return c.do(http.MethodPut, "/apis/apps/v1/replicasets/"+name+"/status", s, nil)
}

func (c *Client) DeleteReplicaSet(name string) error {
	return c.do(http.MethodDelete, "/apis/apps/v1/replicasets/"+name, nil, nil)
}

// ---------- Deployments ----------

func (c *Client) ListDeployments() ([]*api.Deployment, error) {
	var out struct {
		Items []*api.Deployment `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/apps/v1/deployments", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateDeployment(d *api.Deployment) (*api.Deployment, error) {
	var out api.Deployment
	if err := c.do(http.MethodPost, "/apis/apps/v1/deployments", d, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateDeploymentStatus(name string, s api.DeploymentStatus) error {
	return c.do(http.MethodPut, "/apis/apps/v1/deployments/"+name+"/status", s, nil)
}

func (c *Client) DeleteDeployment(name string) error {
	return c.do(http.MethodDelete, "/apis/apps/v1/deployments/"+name, nil, nil)
}

// ---------- Services ----------

func (c *Client) ListServices() ([]*api.Service, error) {
	var out struct {
		Items []*api.Service `json:"items"`
	}
	if err := c.do(http.MethodGet, "/api/v1/services", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetService(name string) (*api.Service, error) {
	var out api.Service
	if err := c.do(http.MethodGet, "/api/v1/services/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateService(s *api.Service) (*api.Service, error) {
	var out api.Service
	if err := c.do(http.MethodPost, "/api/v1/services", s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteService(name string) error {
	return c.do(http.MethodDelete, "/api/v1/services/"+name, nil, nil)
}

// ---------- Endpoints ----------

func (c *Client) ListEndpoints() ([]*api.Endpoints, error) {
	var out struct {
		Items []*api.Endpoints `json:"items"`
	}
	if err := c.do(http.MethodGet, "/api/v1/endpoints", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetEndpoints(name string) (*api.Endpoints, error) {
	var out api.Endpoints
	if err := c.do(http.MethodGet, "/api/v1/endpoints/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpsertEndpoints(ep *api.Endpoints) error {
	return c.do(http.MethodPost, "/api/v1/endpoints", ep, nil)
}

// ---------- Nodes (M4) ----------

func (c *Client) ListNodes() ([]*api.Node, error) {
	var out struct {
		Items []*api.Node `json:"items"`
	}
	if err := c.do(http.MethodGet, "/api/v1/nodes", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) RegisterNode(n *api.Node) (*api.Node, error) {
	var out api.Node
	if err := c.do(http.MethodPost, "/api/v1/nodes", n, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteNode(name string) error {
	return c.do(http.MethodDelete, "/api/v1/nodes/"+name, nil, nil)
}

// ---------- CRDs + Custom Resources (M4) ----------

func (c *Client) ListCRDs() ([]*api.CRD, error) {
	var out struct {
		Items []*api.CRD `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/mk.io/v1/customresourcedefinitions", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetCRD(name string) (*api.CRD, error) {
	var out api.CRD
	if err := c.do(http.MethodGet, "/apis/mk.io/v1/customresourcedefinitions/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateCRD(crd *api.CRD) (*api.CRD, error) {
	var out api.CRD
	if err := c.do(http.MethodPost, "/apis/mk.io/v1/customresourcedefinitions", crd, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteCRD(name string) error {
	return c.do(http.MethodDelete, "/apis/mk.io/v1/customresourcedefinitions/"+name, nil, nil)
}

// ListCustomResources lists CRs for the given CRD object name (e.g.
// "databases.mk.io"). The path is the dynamic REST path under
// /apis/{group}/{version}/{plural}.
func (c *Client) ListCustomResources(crdName string) ([]*api.CustomResource, error) {
	crd, err := c.GetCRD(crdName)
	if err != nil {
		return nil, err
	}
	path := "/apis/" + crd.Spec.Group + "/" + crd.Spec.Version + "/" + crd.Spec.Names.Plural
	var out struct {
		Items []*api.CustomResource `json:"items"`
	}
	if err := c.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateCustomResource(group, version, plural string, cr *api.CustomResource) (*api.CustomResource, error) {
	var out api.CustomResource
	path := "/apis/" + group + "/" + version + "/" + plural
	if err := c.do(http.MethodPost, path, cr, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteCustomResource(group, version, plural, name string) error {
	path := "/apis/" + group + "/" + version + "/" + plural + "/" + name
	return c.do(http.MethodDelete, path, nil, nil)
}

// ---------- RBAC (M4) ----------

func (c *Client) CreateUser(u *api.User) error {
	return c.do(http.MethodPost, "/apis/rbac.mk.io/v1/users", u, nil)
}

func (c *Client) ListUsers() ([]*api.User, error) {
	var out struct {
		Items []*api.User `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/rbac.mk.io/v1/users", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateRole(r *api.Role) error {
	return c.do(http.MethodPost, "/apis/rbac.mk.io/v1/roles", r, nil)
}

func (c *Client) CreateClusterRole(r *api.Role) error {
	return c.do(http.MethodPost, "/apis/rbac.mk.io/v1/clusterroles", r, nil)
}

func (c *Client) CreateRoleBinding(b *api.RoleBinding) error {
	return c.do(http.MethodPost, "/apis/rbac.mk.io/v1/rolebindings", b, nil)
}

func (c *Client) CreateClusterRoleBinding(b *api.RoleBinding) error {
	return c.do(http.MethodPost, "/apis/rbac.mk.io/v1/clusterrolebindings", b, nil)
}

// ---------- M5: Deployments get ----------

func (c *Client) GetDeployment(name string) (*api.Deployment, error) {
	var out api.Deployment
	if err := c.do(http.MethodGet, "/apis/apps/v1/deployments/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- M5: Admission Webhooks ----------

func (c *Client) CreateWebhook(w *api.AdmissionWebhook) error {
	return c.do(http.MethodPost, "/apis/admission.mk.io/v1/webhooks", w, nil)
}

func (c *Client) ListWebhooks() ([]*api.AdmissionWebhook, error) {
	var out struct {
		Items []*api.AdmissionWebhook `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/admission.mk.io/v1/webhooks", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) DeleteWebhook(name string) error {
	return c.do(http.MethodDelete, "/apis/admission.mk.io/v1/webhooks/"+name, nil, nil)
}

// ---------- M5: HPA ----------

func (c *Client) CreateHPA(h *api.HPA) (*api.HPA, error) {
	var out api.HPA
	if err := c.do(http.MethodPost, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers", h, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListHPAs() ([]*api.HPA, error) {
	var out struct {
		Items []*api.HPA `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetHPA(name string) (*api.HPA, error) {
	var out api.HPA
	if err := c.do(http.MethodGet, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateHPAStatus(name string, st api.HPAStatus) error {
	return c.do(http.MethodPut, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers/"+name+"/status", st, nil)
}

func (c *Client) DeleteHPA(name string) error {
	return c.do(http.MethodDelete, "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers/"+name, nil, nil)
}

// ---------- M5: Round-robin client-side load balancer ----------

// NewLB returns a Client that round-robins requests across multiple
// apiserver replicas. Its http.Client uses a custom RoundTripper that
// rewrites req.URL.Host to the next base on every call, so every Client
// method call lands on a different replica. If a replica returns a
// transport error, the caller sees it — retry is up to the caller.
//
// Passing a single base is equivalent to New(bases[0]).
func NewLB(bases []string) *Client {
	if len(bases) == 0 {
		return New("http://127.0.0.1:8080")
	}
	if len(bases) == 1 {
		return New(bases[0])
	}
	idx := new(uint64)
	tr := &rrTransport{bases: bases, idx: idx, next: http.DefaultTransport}
	return &Client{
		Base: bases[0],
		HTTP: &http.Client{Transport: tr, Timeout: 10 * time.Second},
	}
}

// rrTransport rewrites every outgoing request's scheme/host to the next
// base in round-robin order. Used by NewLB.
type rrTransport struct {
	bases []string
	idx   *uint64
	next  http.RoundTripper
}

func (t *rrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := atomic.AddUint64(t.idx, 1)
	base := t.bases[int(n-1)%len(t.bases)]
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.Host = u.Host
	return t.next.RoundTrip(req)
}

