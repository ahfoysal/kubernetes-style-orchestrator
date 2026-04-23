package api

import "time"

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

// Container is a minimal container spec.
type Container struct {
	Name    string          `json:"name" yaml:"name"`
	Image   string          `json:"image" yaml:"image"`
	Command []string        `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string        `json:"args,omitempty" yaml:"args,omitempty"`
	Ports   []ContainerPort `json:"ports,omitempty" yaml:"ports,omitempty"`
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
