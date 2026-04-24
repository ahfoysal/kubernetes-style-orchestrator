package api

import (
	"encoding/json"
	"time"
)

// Phase represents the Pod lifecycle phase.
const (
	PhasePending   = "Pending"
	PhaseScheduled = "Scheduled"
	PhaseRunning   = "Running"
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
)

// ContainerPort describes a port the container exposes.
type ContainerPort struct {
	Name          string `json:"name,omitempty" yaml:"name,omitempty"`
	ContainerPort int    `json:"containerPort" yaml:"containerPort"`
	Protocol      string `json:"protocol,omitempty" yaml:"protocol,omitempty"` // TCP (default)
}

// ResourceList describes resource requests/limits. Values are free-form
// strings (e.g. "100m", "256Mi") — admission webhooks check presence only.
type ResourceList map[string]string

// ResourceRequirements is the {requests,limits} block on a container.
type ResourceRequirements struct {
	Requests ResourceList `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits   ResourceList `json:"limits,omitempty" yaml:"limits,omitempty"`
}

// Container is a minimal container spec.
type Container struct {
	Name      string               `json:"name" yaml:"name"`
	Image     string               `json:"image" yaml:"image"`
	Command   []string             `json:"command,omitempty" yaml:"command,omitempty"`
	Args      []string             `json:"args,omitempty" yaml:"args,omitempty"`
	Ports     []ContainerPort      `json:"ports,omitempty" yaml:"ports,omitempty"`
	Resources ResourceRequirements `json:"resources,omitempty" yaml:"resources,omitempty"`
}

// PodSpec describes desired state of a Pod.
type PodSpec struct {
	Containers []Container `json:"containers" yaml:"containers"`
}

// PodStatus describes observed state.
type PodStatus struct {
	Phase       string    `json:"phase" yaml:"phase"`
	NodeName    string    `json:"nodeName,omitempty" yaml:"nodeName,omitempty"`
	ContainerID string    `json:"containerId,omitempty" yaml:"containerId,omitempty"`
	Message     string    `json:"message,omitempty" yaml:"message,omitempty"`
	// PodIP is the pod's reachable address (the host IP, since we run
	// on the host network for this MVP).
	PodIP string `json:"podIP,omitempty" yaml:"podIP,omitempty"`
	// HostPort is the reachable TCP port for the pod's first container.
	// The stub runtime assigns this dynamically; real container runtimes
	// would report the published port here.
	HostPort    int       `json:"hostPort,omitempty" yaml:"hostPort,omitempty"`
	LastUpdated time.Time `json:"lastUpdated" yaml:"lastUpdated"`
}

// Metadata is object metadata.
type Metadata struct {
	Name      string            `json:"name" yaml:"name"`
	Namespace string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// Pod is the top-level Pod object.
type Pod struct {
	APIVersion string    `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Spec       PodSpec   `json:"spec" yaml:"spec"`
	Status     PodStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// PodTemplate is the template used by ReplicaSet / Deployment to create pods.
type PodTemplate struct {
	Metadata Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Spec     PodSpec  `json:"spec" yaml:"spec"`
}

// ReplicaSetSpec describes desired state of a ReplicaSet.
type ReplicaSetSpec struct {
	Replicas int               `json:"replicas" yaml:"replicas"`
	Selector map[string]string `json:"selector,omitempty" yaml:"selector,omitempty"`
	Template PodTemplate       `json:"template" yaml:"template"`
}

// ReplicaSetStatus is observed state of a ReplicaSet.
type ReplicaSetStatus struct {
	Replicas      int       `json:"replicas" yaml:"replicas"`
	ReadyReplicas int       `json:"readyReplicas" yaml:"readyReplicas"`
	LastUpdated   time.Time `json:"lastUpdated" yaml:"lastUpdated"`
}

// ReplicaSet is a set of identical pods.
type ReplicaSet struct {
	APIVersion string           `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string           `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata         `json:"metadata" yaml:"metadata"`
	Spec       ReplicaSetSpec   `json:"spec" yaml:"spec"`
	Status     ReplicaSetStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// DeploymentSpec describes desired state of a Deployment.
type DeploymentSpec struct {
	Replicas int               `json:"replicas" yaml:"replicas"`
	Selector map[string]string `json:"selector,omitempty" yaml:"selector,omitempty"`
	Template PodTemplate       `json:"template" yaml:"template"`
	// Strategy is a stub for M3 (RollingUpdate). Currently only "Recreate"
	// semantics are actually implemented by the controller.
	Strategy string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
}

// DeploymentStatus is observed state.
type DeploymentStatus struct {
	Replicas      int       `json:"replicas" yaml:"replicas"`
	ReadyReplicas int       `json:"readyReplicas" yaml:"readyReplicas"`
	LastUpdated   time.Time `json:"lastUpdated" yaml:"lastUpdated"`
}

// Deployment declares desired replicas of a pod template.
type Deployment struct {
	APIVersion string           `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string           `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata         `json:"metadata" yaml:"metadata"`
	Spec       DeploymentSpec   `json:"spec" yaml:"spec"`
	Status     DeploymentStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// Service types.
const (
	ServiceTypeClusterIP = "ClusterIP"
)

// ServicePort describes a port exposed by a Service.
type ServicePort struct {
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	Port       int    `json:"port" yaml:"port"`
	TargetPort int    `json:"targetPort,omitempty" yaml:"targetPort,omitempty"`
	Protocol   string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
}

// ServiceSpec describes desired state of a Service.
type ServiceSpec struct {
	Selector  map[string]string `json:"selector,omitempty" yaml:"selector,omitempty"`
	Ports     []ServicePort     `json:"ports" yaml:"ports"`
	Type      string            `json:"type,omitempty" yaml:"type,omitempty"`
	ClusterIP string            `json:"clusterIP,omitempty" yaml:"clusterIP,omitempty"`
}

// ServiceStatus is observed state of a Service.
type ServiceStatus struct {
	LastUpdated time.Time `json:"lastUpdated" yaml:"lastUpdated"`
}

// Service exposes a logical set of pods under a stable virtual IP.
type Service struct {
	APIVersion string        `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata      `json:"metadata" yaml:"metadata"`
	Spec       ServiceSpec   `json:"spec" yaml:"spec"`
	Status     ServiceStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// EndpointAddress is a single backend address for a Service.
type EndpointAddress struct {
	IP       string `json:"ip" yaml:"ip"`
	Port     int    `json:"port" yaml:"port"`
	NodeName string `json:"nodeName,omitempty" yaml:"nodeName,omitempty"`
	PodName  string `json:"podName,omitempty" yaml:"podName,omitempty"`
}

// Endpoints is the set of live backends currently serving a Service.
// Name matches the Service name.
type Endpoints struct {
	APIVersion string            `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string            `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata          `json:"metadata" yaml:"metadata"`
	Subsets    []EndpointAddress `json:"subsets" yaml:"subsets"`
	LastUpdated time.Time        `json:"lastUpdated,omitempty" yaml:"lastUpdated,omitempty"`
}

// ---------------- Node (M4: multi-node) ----------------

// NodeStatus is observed state of a Node.
type NodeStatus struct {
	// Ready is true if the kubelet has checked in within the TTL.
	Ready         bool      `json:"ready" yaml:"ready"`
	LastHeartbeat time.Time `json:"lastHeartbeat" yaml:"lastHeartbeat"`
	// PodCount is the current number of pods scheduled to this node.
	PodCount int `json:"podCount" yaml:"podCount"`
	Address  string `json:"address,omitempty" yaml:"address,omitempty"`
}

// Node represents a registered kubelet.
type Node struct {
	APIVersion string     `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata   `json:"metadata" yaml:"metadata"`
	Status     NodeStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// ---------------- CRD (M4) ----------------

// CRDSchemaProperty is a lightweight JSON-schema-ish property descriptor.
// We only model type+description for the M4 demo — enough for a kubectl-like
// UX without pulling in a full JSON schema validator.
type CRDSchemaProperty struct {
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// CRDSchema describes the shape of a CR's `spec`.
type CRDSchema struct {
	Required   []string                     `json:"required,omitempty" yaml:"required,omitempty"`
	Properties map[string]CRDSchemaProperty `json:"properties,omitempty" yaml:"properties,omitempty"`
}

// CRDNames declares the REST/CLI names for the CR.
type CRDNames struct {
	Kind     string `json:"kind" yaml:"kind"`
	Plural   string `json:"plural" yaml:"plural"`
	Singular string `json:"singular,omitempty" yaml:"singular,omitempty"`
}

// CRDSpec describes a single CustomResourceDefinition.
type CRDSpec struct {
	Group   string    `json:"group" yaml:"group"`
	Version string    `json:"version" yaml:"version"`
	Names   CRDNames  `json:"names" yaml:"names"`
	Schema  CRDSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
	Scope   string    `json:"scope,omitempty" yaml:"scope,omitempty"` // Namespaced|Cluster
}

// CustomResourceDefinition is a user-declared resource type.
type CRD struct {
	APIVersion string    `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Spec       CRDSpec   `json:"spec" yaml:"spec"`
}

// CustomResource is any instance of a CRD-declared type. The `Object`
// field holds the raw YAML/JSON body verbatim (sans apiVersion/kind/
// metadata). Operators decode it themselves against the CRD schema.
type CustomResource struct {
	APIVersion string                 `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string                 `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata               `json:"metadata" yaml:"metadata"`
	Spec       map[string]interface{} `json:"spec,omitempty" yaml:"spec,omitempty"`
	Status     map[string]interface{} `json:"status,omitempty" yaml:"status,omitempty"`
}

// ---------------- RBAC (M4) ----------------

// PolicyRule is an RBAC rule: which verbs are allowed on which resources.
// APIGroups of "*" matches any group; Resources of "*" matches any resource;
// Verbs of "*" matches any verb.
type PolicyRule struct {
	APIGroups []string `json:"apiGroups,omitempty" yaml:"apiGroups,omitempty"`
	Resources []string `json:"resources,omitempty" yaml:"resources,omitempty"`
	Verbs     []string `json:"verbs,omitempty" yaml:"verbs,omitempty"`
}

// Role is a namespaced set of PolicyRules.
type Role struct {
	APIVersion string       `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata     `json:"metadata" yaml:"metadata"`
	Rules      []PolicyRule `json:"rules" yaml:"rules"`
}

// ClusterRole is a cluster-scoped Role.
type ClusterRole = Role

// RoleRef references a Role or ClusterRole by name+kind.
type RoleRef struct {
	Kind string `json:"kind" yaml:"kind"` // Role|ClusterRole
	Name string `json:"name" yaml:"name"`
}

// Subject is the principal a binding grants a role to.
type Subject struct {
	Kind string `json:"kind" yaml:"kind"` // User|Group|ServiceAccount
	Name string `json:"name" yaml:"name"`
}

// RoleBinding attaches a role to subjects.
type RoleBinding struct {
	APIVersion string    `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Subjects   []Subject `json:"subjects" yaml:"subjects"`
	RoleRef    RoleRef   `json:"roleRef" yaml:"roleRef"`
}

// ClusterRoleBinding is the cluster-scoped RoleBinding.
type ClusterRoleBinding = RoleBinding

// ---------------- M5: Admission Webhooks ----------------

// AdmissionReviewRequest is the body POSTed to a webhook before a resource
// is persisted. Kind identifies the object kind, Operation is "CREATE" or
// "UPDATE", and Object is the raw resource JSON.
type AdmissionReviewRequest struct {
	UID       string          `json:"uid"`
	Kind      string          `json:"kind"`
	Resource  string          `json:"resource"`
	Namespace string          `json:"namespace,omitempty"`
	Name      string          `json:"name,omitempty"`
	Operation string          `json:"operation"`
	Object    json.RawMessage `json:"object"`
}

// AdmissionReviewResponse is what a webhook returns. If Allowed is false,
// the request is rejected with Message. If Patch is non-empty, it replaces
// the incoming object before persistence (mutating webhook).
type AdmissionReviewResponse struct {
	UID     string          `json:"uid"`
	Allowed bool            `json:"allowed"`
	Message string          `json:"message,omitempty"`
	Patch   json.RawMessage `json:"patch,omitempty"`
}

// AdmissionWebhookType is mutating or validating.
const (
	WebhookTypeMutating   = "Mutating"
	WebhookTypeValidating = "Validating"
)

// AdmissionWebhook registers a remote webhook endpoint that the apiserver
// consults before persisting resources.
type AdmissionWebhook struct {
	APIVersion string   `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata `json:"metadata" yaml:"metadata"`
	// Type is Mutating or Validating.
	Type string `json:"type" yaml:"type"`
	// URL is the full webhook URL (http://host:port/path).
	URL string `json:"url" yaml:"url"`
	// Resources are the lowercase plurals this webhook cares about
	// (e.g. ["pods","deployments"]). "*" matches any resource.
	Resources []string `json:"resources" yaml:"resources"`
	// FailurePolicy is "Fail" (deny on webhook error) or "Ignore".
	FailurePolicy string `json:"failurePolicy,omitempty" yaml:"failurePolicy,omitempty"`
}

// ---------------- M5: HPA ----------------

// HPAMetric describes the target metric. Only "cpu" with averageUtilization
// is supported in M5.
type HPAMetric struct {
	Type               string `json:"type" yaml:"type"` // Resource
	Name               string `json:"name" yaml:"name"` // cpu
	TargetAverageValue int    `json:"targetAverageUtilization" yaml:"targetAverageUtilization"`
}

// HPASpec describes desired HPA behavior.
type HPASpec struct {
	ScaleTargetRef struct {
		Kind string `json:"kind" yaml:"kind"`
		Name string `json:"name" yaml:"name"`
	} `json:"scaleTargetRef" yaml:"scaleTargetRef"`
	MinReplicas int       `json:"minReplicas" yaml:"minReplicas"`
	MaxReplicas int       `json:"maxReplicas" yaml:"maxReplicas"`
	Metric      HPAMetric `json:"metric" yaml:"metric"`
}

// HPAStatus is the observed state.
type HPAStatus struct {
	CurrentReplicas     int       `json:"currentReplicas"`
	DesiredReplicas     int       `json:"desiredReplicas"`
	CurrentAverageValue int       `json:"currentAverageUtilization"`
	LastScaleTime       time.Time `json:"lastScaleTime"`
	LastUpdated         time.Time `json:"lastUpdated"`
}

// HorizontalPodAutoscaler scales a Deployment between min and max replicas
// based on a metric (CPU %).
type HPA struct {
	APIVersion string    `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Spec       HPASpec   `json:"spec" yaml:"spec"`
	Status     HPAStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// PodMetric is the metrics-server view of a pod's resource usage.
type PodMetric struct {
	PodName  string  `json:"podName"`
	NodeName string  `json:"nodeName"`
	CPUPct   float64 `json:"cpuPct"`
}

// ---------------- M6: NetworkPolicy ----------------

// NetPolPeer selects pods by label. Empty PodSelector means "all pods".
type NetPolPeer struct {
	PodSelector map[string]string `json:"podSelector,omitempty" yaml:"podSelector,omitempty"`
}

// NetPolPort describes an allowed port. Protocol defaults to TCP.
type NetPolPort struct {
	Port     int    `json:"port" yaml:"port"`
	Protocol string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
}

// NetPolIngressRule is an allow-list: traffic from any of From whose port
// is in Ports is permitted. Empty From = any source; empty Ports = any port.
type NetPolIngressRule struct {
	From  []NetPolPeer `json:"from,omitempty" yaml:"from,omitempty"`
	Ports []NetPolPort `json:"ports,omitempty" yaml:"ports,omitempty"`
}

// NetPolSpec selects target pods via PodSelector and specifies the set of
// ingress rules that allow traffic to them. A selected pod with no rules
// is effectively isolated (default-deny).
type NetPolSpec struct {
	PodSelector map[string]string   `json:"podSelector" yaml:"podSelector"`
	Ingress     []NetPolIngressRule `json:"ingress,omitempty" yaml:"ingress,omitempty"`
	// PolicyTypes usually ["Ingress"]; we only implement Ingress in M6.
	PolicyTypes []string `json:"policyTypes,omitempty" yaml:"policyTypes,omitempty"`
}

// NetworkPolicy isolates selected pods and allow-lists specific peers.
type NetworkPolicy struct {
	APIVersion string     `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata   `json:"metadata" yaml:"metadata"`
	Spec       NetPolSpec `json:"spec" yaml:"spec"`
}

// ---------------- M6: Multi-cluster federation ----------------

// Cluster is a member cluster reachable from the control plane. The
// control plane lists clusters + their heartbeat status.
type ClusterStatus struct {
	Ready         bool      `json:"ready" yaml:"ready"`
	LastHeartbeat time.Time `json:"lastHeartbeat" yaml:"lastHeartbeat"`
	Message       string    `json:"message,omitempty" yaml:"message,omitempty"`
}

type ClusterSpec struct {
	// Server is the reachable apiserver URL of the member cluster.
	Server string `json:"server" yaml:"server"`
	// Region is an informational label for placement preferences.
	Region string `json:"region,omitempty" yaml:"region,omitempty"`
}

type Cluster struct {
	APIVersion string        `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata      `json:"metadata" yaml:"metadata"`
	Spec       ClusterSpec   `json:"spec" yaml:"spec"`
	Status     ClusterStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// FederatedDeploymentSpec fans a Deployment out to all listed clusters
// (or every registered cluster when Clusters is empty).
type FederatedDeploymentSpec struct {
	Clusters []string       `json:"clusters,omitempty" yaml:"clusters,omitempty"`
	Template DeploymentSpec `json:"template" yaml:"template"`
}

// FederatedDeploymentStatus reports how many clusters are currently in sync.
type FederatedDeploymentStatus struct {
	ClusterCount int       `json:"clusterCount" yaml:"clusterCount"`
	ReadyCount   int       `json:"readyCount" yaml:"readyCount"`
	LastUpdated  time.Time `json:"lastUpdated" yaml:"lastUpdated"`
}

// FederatedDeployment is replicated across member clusters by the federation
// controller. Each member cluster receives a derived Deployment object with
// the same name.
type FederatedDeployment struct {
	APIVersion string                    `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string                    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata                  `json:"metadata" yaml:"metadata"`
	Spec       FederatedDeploymentSpec   `json:"spec" yaml:"spec"`
	Status     FederatedDeploymentStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// User is an identity that authenticates via a bearer token. In a real
// cluster this would come from certs/OIDC; for M4 we store token→user
// in the apiserver's DB.
type User struct {
	APIVersion string   `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata `json:"metadata" yaml:"metadata"`
	// Token is the bearer token presented in Authorization: Bearer <token>.
	Token  string   `json:"token" yaml:"token"`
	Groups []string `json:"groups,omitempty" yaml:"groups,omitempty"`
}
