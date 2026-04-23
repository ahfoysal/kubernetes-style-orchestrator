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

// Container is a minimal container spec.
type Container struct {
	Name    string   `json:"name" yaml:"name"`
	Image   string   `json:"image" yaml:"image"`
	Command []string `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// PodSpec describes desired state of a Pod.
type PodSpec struct {
	Containers []Container `json:"containers" yaml:"containers"`
}

// PodStatus describes observed state.
type PodStatus struct {
	Phase        string    `json:"phase" yaml:"phase"`
	NodeName     string    `json:"nodeName,omitempty" yaml:"nodeName,omitempty"`
	ContainerID  string    `json:"containerId,omitempty" yaml:"containerId,omitempty"`
	Message      string    `json:"message,omitempty" yaml:"message,omitempty"`
	LastUpdated  time.Time `json:"lastUpdated" yaml:"lastUpdated"`
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
