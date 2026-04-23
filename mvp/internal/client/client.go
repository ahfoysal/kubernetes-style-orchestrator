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

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// Client talks to mk-apiserver over REST.
type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{Base: base, HTTP: http.DefaultClient}
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
