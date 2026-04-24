package client

import (
	"net/http"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// ---------- M6: NetworkPolicy ----------

func (c *Client) ListNetworkPolicies() ([]*api.NetworkPolicy, error) {
	var out struct {
		Items []*api.NetworkPolicy `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/networking.mk.io/v1/networkpolicies", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetNetworkPolicy(name string) (*api.NetworkPolicy, error) {
	var out api.NetworkPolicy
	if err := c.do(http.MethodGet, "/apis/networking.mk.io/v1/networkpolicies/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateNetworkPolicy(np *api.NetworkPolicy) (*api.NetworkPolicy, error) {
	var out api.NetworkPolicy
	if err := c.do(http.MethodPost, "/apis/networking.mk.io/v1/networkpolicies", np, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteNetworkPolicy(name string) error {
	return c.do(http.MethodDelete, "/apis/networking.mk.io/v1/networkpolicies/"+name, nil, nil)
}

// ---------- M6: Clusters (federation) ----------

func (c *Client) ListClusters() ([]*api.Cluster, error) {
	var out struct {
		Items []*api.Cluster `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/federation.mk.io/v1/clusters", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateCluster(cl *api.Cluster) (*api.Cluster, error) {
	var out api.Cluster
	if err := c.do(http.MethodPost, "/apis/federation.mk.io/v1/clusters", cl, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateClusterStatus(name string, st api.ClusterStatus) error {
	return c.do(http.MethodPut, "/apis/federation.mk.io/v1/clusters/"+name+"/status", st, nil)
}

func (c *Client) DeleteCluster(name string) error {
	return c.do(http.MethodDelete, "/apis/federation.mk.io/v1/clusters/"+name, nil, nil)
}

// ---------- M6: FederatedDeployment ----------

func (c *Client) ListFederatedDeployments() ([]*api.FederatedDeployment, error) {
	var out struct {
		Items []*api.FederatedDeployment `json:"items"`
	}
	if err := c.do(http.MethodGet, "/apis/federation.mk.io/v1/federateddeployments", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) CreateFederatedDeployment(fd *api.FederatedDeployment) (*api.FederatedDeployment, error) {
	var out api.FederatedDeployment
	if err := c.do(http.MethodPost, "/apis/federation.mk.io/v1/federateddeployments", fd, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateFederatedDeploymentStatus(name string, st api.FederatedDeploymentStatus) error {
	return c.do(http.MethodPut, "/apis/federation.mk.io/v1/federateddeployments/"+name+"/status", st, nil)
}

func (c *Client) DeleteFederatedDeployment(name string) error {
	return c.do(http.MethodDelete, "/apis/federation.mk.io/v1/federateddeployments/"+name, nil, nil)
}
