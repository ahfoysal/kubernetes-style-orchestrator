package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

func main() {
	server := flag.String("server", envOr("MK_SERVER", "http://127.0.0.1:8080"), "apiserver URL")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "apply":
		applyCmd(*server, rest)
	case "get":
		getCmd(*server, rest)
	case "describe":
		describeCmd(*server, rest)
	case "delete":
		deleteCmd(*server, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `mkctl - tiny kubectl-like CLI

Usage:
  mkctl [--server=URL] <command> [args]

Commands:
  apply -f <file.yaml>              Create or update a Pod/ReplicaSet/Deployment
  get pods                          List pods
  get replicasets | get rs          List replicasets
  get deployments | get deploy      List deployments
  describe pod <name>               Show a pod (JSON)
  delete pod <name>                 Delete a pod
  delete rs <name>                  Delete a replicaset
  delete deploy <name>              Delete a deployment

Env:
  MK_SERVER                         Default server URL
`)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// kindPeek lets us detect a document's Kind before unmarshaling into the
// concrete typed struct.
type kindPeek struct {
	Kind string `yaml:"kind"`
}

func applyCmd(server string, args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	file := fs.String("f", "", "YAML file")
	fs.Parse(args)
	if *file == "" {
		fmt.Fprintln(os.Stderr, "apply: -f <file> required")
		os.Exit(2)
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		die("read file: %v", err)
	}
	var peek kindPeek
	if err := yaml.Unmarshal(data, &peek); err != nil {
		die("parse yaml kind: %v", err)
	}
	switch peek.Kind {
	case "", "Pod":
		applyPod(server, data)
	case "ReplicaSet":
		applyReplicaSet(server, data)
	case "Deployment":
		applyDeployment(server, data)
	case "Service":
		applyService(server, data)
	case "AdmissionWebhook":
		applyWebhook(server, data)
	case "HorizontalPodAutoscaler":
		applyHPA(server, data)
	case "NetworkPolicy":
		applyNetworkPolicy(server, data)
	case "Cluster":
		applyCluster(server, data)
	case "FederatedDeployment":
		applyFederatedDeployment(server, data)
	default:
		die("unsupported kind %q", peek.Kind)
	}
}

func applyNetworkPolicy(server string, data []byte) {
	var np api.NetworkPolicy
	if err := yaml.Unmarshal(data, &np); err != nil {
		die("parse yaml: %v", err)
	}
	if np.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(np)
	resp, err := http.Post(server+"/apis/networking.mk.io/v1/networkpolicies", "application/json", bytes.NewReader(body))
	check(resp, err, "apply networkpolicy")
	defer resp.Body.Close()
	fmt.Printf("networkpolicy/%s applied\n", np.Metadata.Name)
}

func applyCluster(server string, data []byte) {
	var c api.Cluster
	if err := yaml.Unmarshal(data, &c); err != nil {
		die("parse yaml: %v", err)
	}
	if c.Metadata.Name == "" || c.Spec.Server == "" {
		die("metadata.name and spec.server required")
	}
	body, _ := json.Marshal(c)
	resp, err := http.Post(server+"/apis/federation.mk.io/v1/clusters", "application/json", bytes.NewReader(body))
	check(resp, err, "apply cluster")
	defer resp.Body.Close()
	fmt.Printf("cluster/%s applied (server=%s)\n", c.Metadata.Name, c.Spec.Server)
}

func applyFederatedDeployment(server string, data []byte) {
	var fd api.FederatedDeployment
	if err := yaml.Unmarshal(data, &fd); err != nil {
		die("parse yaml: %v", err)
	}
	if fd.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(fd)
	resp, err := http.Post(server+"/apis/federation.mk.io/v1/federateddeployments", "application/json", bytes.NewReader(body))
	check(resp, err, "apply federateddeployment")
	defer resp.Body.Close()
	fmt.Printf("federateddeployment/%s applied (clusters=%v)\n", fd.Metadata.Name, fd.Spec.Clusters)
}

func applyService(server string, data []byte) {
	var s api.Service
	if err := yaml.Unmarshal(data, &s); err != nil {
		die("parse yaml: %v", err)
	}
	if s.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(s)
	resp, err := http.Post(server+"/api/v1/services", "application/json", bytes.NewReader(body))
	check(resp, err, "apply service")
	defer resp.Body.Close()
	var out api.Service
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("service/%s applied (clusterIP=%s)\n", out.Metadata.Name, out.Spec.ClusterIP)
}

func applyPod(server string, data []byte) {
	var p api.Pod
	if err := yaml.Unmarshal(data, &p); err != nil {
		die("parse yaml: %v", err)
	}
	if p.Metadata.Name == "" {
		die("metadata.name required")
	}
	if p.Kind == "" {
		p.Kind = "Pod"
	}
	body, _ := json.Marshal(p)
	resp, err := http.Post(server+"/api/v1/pods", "application/json", bytes.NewReader(body))
	check(resp, err, "apply pod")
	defer resp.Body.Close()
	var out api.Pod
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("pod/%s applied (phase=%s)\n", out.Metadata.Name, out.Status.Phase)
}

func applyReplicaSet(server string, data []byte) {
	var rs api.ReplicaSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		die("parse yaml: %v", err)
	}
	if rs.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(rs)
	resp, err := http.Post(server+"/apis/apps/v1/replicasets", "application/json", bytes.NewReader(body))
	check(resp, err, "apply replicaset")
	defer resp.Body.Close()
	var out api.ReplicaSet
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("replicaset/%s applied (replicas=%d)\n", out.Metadata.Name, out.Spec.Replicas)
}

func applyWebhook(server string, data []byte) {
	var h api.AdmissionWebhook
	if err := yaml.Unmarshal(data, &h); err != nil {
		die("parse yaml: %v", err)
	}
	if h.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(h)
	resp, err := http.Post(server+"/apis/admission.mk.io/v1/webhooks", "application/json", bytes.NewReader(body))
	check(resp, err, "apply webhook")
	defer resp.Body.Close()
	fmt.Printf("admissionwebhook/%s applied (type=%s url=%s)\n", h.Metadata.Name, h.Type, h.URL)
}

func applyHPA(server string, data []byte) {
	var h api.HPA
	if err := yaml.Unmarshal(data, &h); err != nil {
		die("parse yaml: %v", err)
	}
	if h.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(h)
	resp, err := http.Post(server+"/apis/autoscaling.mk.io/v1/horizontalpodautoscalers", "application/json", bytes.NewReader(body))
	check(resp, err, "apply hpa")
	defer resp.Body.Close()
	var out api.HPA
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("hpa/%s applied (target=%s/%s min=%d max=%d)\n", out.Metadata.Name, out.Spec.ScaleTargetRef.Kind, out.Spec.ScaleTargetRef.Name, out.Spec.MinReplicas, out.Spec.MaxReplicas)
}

func applyDeployment(server string, data []byte) {
	var d api.Deployment
	if err := yaml.Unmarshal(data, &d); err != nil {
		die("parse yaml: %v", err)
	}
	if d.Metadata.Name == "" {
		die("metadata.name required")
	}
	body, _ := json.Marshal(d)
	resp, err := http.Post(server+"/apis/apps/v1/deployments", "application/json", bytes.NewReader(body))
	check(resp, err, "apply deployment")
	defer resp.Body.Close()
	var out api.Deployment
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("deployment/%s applied (replicas=%d)\n", out.Metadata.Name, out.Spec.Replicas)
}

func getCmd(server string, args []string) {
	if len(args) < 1 {
		die("usage: mkctl get pods|replicasets|deployments")
	}
	switch args[0] {
	case "pods", "pod":
		getPods(server)
	case "replicasets", "rs":
		getReplicaSets(server)
	case "deployments", "deploy", "deployment":
		getDeployments(server)
	case "services", "svc", "service":
		getServices(server)
	case "endpoints", "ep", "endpoint":
		getEndpoints(server)
	case "hpa", "horizontalpodautoscalers":
		getHPAs(server)
	case "webhooks", "admissionwebhooks":
		getWebhooks(server)
	case "networkpolicies", "netpol", "networkpolicy":
		getNetworkPolicies(server)
	case "clusters", "cluster":
		getClusters(server)
	case "federateddeployments", "fd", "federateddeployment":
		getFederatedDeployments(server)
	default:
		die("unknown resource %q", args[0])
	}
}

func getNetworkPolicies(server string) {
	resp, err := http.Get(server + "/apis/networking.mk.io/v1/networkpolicies")
	check(resp, err, "get networkpolicies")
	defer resp.Body.Close()
	var list struct {
		Items []*api.NetworkPolicy `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSELECTOR\tINGRESS-RULES")
	for _, np := range list.Items {
		sel := ""
		for k, v := range np.Spec.PodSelector {
			if sel != "" {
				sel += ","
			}
			sel += k + "=" + v
		}
		if sel == "" {
			sel = "<all>"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\n", np.Metadata.Name, sel, len(np.Spec.Ingress))
	}
	tw.Flush()
}

func getClusters(server string) {
	resp, err := http.Get(server + "/apis/federation.mk.io/v1/clusters")
	check(resp, err, "get clusters")
	defer resp.Body.Close()
	var list struct {
		Items []*api.Cluster `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSERVER\tREGION\tREADY\tLAST-HEARTBEAT")
	now := time.Now()
	for _, c := range list.Items {
		hb := "<never>"
		if !c.Status.LastHeartbeat.IsZero() {
			hb = now.Sub(c.Status.LastHeartbeat).Truncate(time.Second).String() + " ago"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\n",
			c.Metadata.Name, c.Spec.Server, nz(c.Spec.Region, "-"), c.Status.Ready, hb)
	}
	tw.Flush()
}

func getFederatedDeployments(server string) {
	resp, err := http.Get(server + "/apis/federation.mk.io/v1/federateddeployments")
	check(resp, err, "get federateddeployments")
	defer resp.Body.Close()
	var list struct {
		Items []*api.FederatedDeployment `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tREPLICAS\tCLUSTERS\tREADY-CLUSTERS")
	for _, fd := range list.Items {
		target := "<all>"
		if len(fd.Spec.Clusters) > 0 {
			target = fmt.Sprintf("%v", fd.Spec.Clusters)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d/%d\n",
			fd.Metadata.Name, fd.Spec.Template.Replicas, target,
			fd.Status.ReadyCount, fd.Status.ClusterCount)
	}
	tw.Flush()
}

func getServices(server string) {
	resp, err := http.Get(server + "/api/v1/services")
	check(resp, err, "get services")
	defer resp.Body.Close()
	var list struct {
		Items []*api.Service `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tCLUSTER-IP\tPORT(S)\tSELECTOR\tAGE")
	now := time.Now()
	for _, s := range list.Items {
		ports := ""
		for i, p := range s.Spec.Ports {
			if i > 0 {
				ports += ","
			}
			tp := p.TargetPort
			if tp == 0 {
				tp = p.Port
			}
			proto := p.Protocol
			if proto == "" {
				proto = "TCP"
			}
			ports += fmt.Sprintf("%d->%d/%s", p.Port, tp, proto)
		}
		sel := ""
		for k, v := range s.Spec.Selector {
			if sel != "" {
				sel += ","
			}
			sel += k + "=" + v
		}
		age := now.Sub(s.Status.LastUpdated).Truncate(time.Second).String()
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Metadata.Name, nz(s.Spec.Type, "ClusterIP"), s.Spec.ClusterIP, ports, sel, age)
	}
	tw.Flush()
}

func getEndpoints(server string) {
	resp, err := http.Get(server + "/api/v1/endpoints")
	check(resp, err, "get endpoints")
	defer resp.Body.Close()
	var list struct {
		Items []*api.Endpoints `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENDPOINTS\tAGE")
	now := time.Now()
	for _, e := range list.Items {
		addrs := ""
		for i, a := range e.Subsets {
			if i > 0 {
				addrs += ","
			}
			addrs += fmt.Sprintf("%s:%d", a.IP, a.Port)
		}
		if addrs == "" {
			addrs = "<none>"
		}
		age := now.Sub(e.LastUpdated).Truncate(time.Second).String()
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Metadata.Name, addrs, age)
	}
	tw.Flush()
}

func getPods(server string) {
	resp, err := http.Get(server + "/api/v1/pods")
	check(resp, err, "get pods")
	defer resp.Body.Close()
	var list struct {
		Items []*api.Pod `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tNODE\tIMAGE\tAGE\tMESSAGE")
	now := time.Now()
	for _, p := range list.Items {
		img := ""
		if len(p.Spec.Containers) > 0 {
			img = p.Spec.Containers[0].Image
		}
		age := now.Sub(p.Status.LastUpdated).Truncate(time.Second).String()
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Metadata.Name, p.Status.Phase, nz(p.Status.NodeName, "<none>"), img, age, p.Status.Message)
	}
	tw.Flush()
}

func getReplicaSets(server string) {
	resp, err := http.Get(server + "/apis/apps/v1/replicasets")
	check(resp, err, "get replicasets")
	defer resp.Body.Close()
	var list struct {
		Items []*api.ReplicaSet `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESIRED\tCURRENT\tREADY\tIMAGE\tAGE")
	now := time.Now()
	for _, rs := range list.Items {
		img := ""
		if len(rs.Spec.Template.Spec.Containers) > 0 {
			img = rs.Spec.Template.Spec.Containers[0].Image
		}
		age := now.Sub(rs.Status.LastUpdated).Truncate(time.Second).String()
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\n",
			rs.Metadata.Name, rs.Spec.Replicas, rs.Status.Replicas, rs.Status.ReadyReplicas, img, age)
	}
	tw.Flush()
}

func getDeployments(server string) {
	resp, err := http.Get(server + "/apis/apps/v1/deployments")
	check(resp, err, "get deployments")
	defer resp.Body.Close()
	var list struct {
		Items []*api.Deployment `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDESIRED\tCURRENT\tREADY\tIMAGE\tAGE")
	now := time.Now()
	for _, d := range list.Items {
		img := ""
		if len(d.Spec.Template.Spec.Containers) > 0 {
			img = d.Spec.Template.Spec.Containers[0].Image
		}
		age := now.Sub(d.Status.LastUpdated).Truncate(time.Second).String()
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\n",
			d.Metadata.Name, d.Spec.Replicas, d.Status.Replicas, d.Status.ReadyReplicas, img, age)
	}
	tw.Flush()
}

func getHPAs(server string) {
	resp, err := http.Get(server + "/apis/autoscaling.mk.io/v1/horizontalpodautoscalers")
	check(resp, err, "get hpa")
	defer resp.Body.Close()
	var list struct {
		Items []*api.HPA `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTARGET\tMIN\tMAX\tCURRENT\tDESIRED\tCPU%\tLAST-SCALE")
	for _, h := range list.Items {
		last := "<never>"
		if !h.Status.LastScaleTime.IsZero() {
			last = time.Since(h.Status.LastScaleTime).Truncate(time.Second).String() + " ago"
		}
		fmt.Fprintf(tw, "%s\t%s/%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			h.Metadata.Name,
			h.Spec.ScaleTargetRef.Kind, h.Spec.ScaleTargetRef.Name,
			h.Spec.MinReplicas, h.Spec.MaxReplicas,
			h.Status.CurrentReplicas, h.Status.DesiredReplicas,
			h.Status.CurrentAverageValue, last)
	}
	tw.Flush()
}

func getWebhooks(server string) {
	resp, err := http.Get(server + "/apis/admission.mk.io/v1/webhooks")
	check(resp, err, "get webhooks")
	defer resp.Body.Close()
	var list struct {
		Items []*api.AdmissionWebhook `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		die("decode: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tRESOURCES\tURL\tFAILURE-POLICY")
	for _, h := range list.Items {
		res := ""
		for i, r := range h.Resources {
			if i > 0 {
				res += ","
			}
			res += r
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Metadata.Name, h.Type, res, h.URL, h.FailurePolicy)
	}
	tw.Flush()
}

func describeCmd(server string, args []string) {
	if len(args) < 2 || args[0] != "pod" {
		die("usage: mkctl describe pod <name>")
	}
	name := args[1]
	resp, err := http.Get(server + "/api/v1/pods/" + name)
	check(resp, err, "describe")
	defer resp.Body.Close()
	var p api.Pod
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		die("decode: %v", err)
	}
	out, _ := json.MarshalIndent(p, "", "  ")
	fmt.Println(string(out))
}

func deleteCmd(server string, args []string) {
	if len(args) < 2 {
		die("usage: mkctl delete pod|rs|deploy <name>")
	}
	kind := args[0]
	name := args[1]
	var path string
	switch kind {
	case "pod":
		path = "/api/v1/pods/" + name
	case "rs", "replicaset", "replicasets":
		path = "/apis/apps/v1/replicasets/" + name
	case "deploy", "deployment", "deployments":
		path = "/apis/apps/v1/deployments/" + name
	case "svc", "service", "services":
		path = "/api/v1/services/" + name
	default:
		die("unknown kind %q", kind)
	}
	req, _ := http.NewRequest(http.MethodDelete, server+path, nil)
	resp, err := http.DefaultClient.Do(req)
	check(resp, err, "delete")
	defer resp.Body.Close()
	fmt.Printf("%s/%s deleted\n", kind, name)
}

func check(resp *http.Response, err error, op string) {
	if err != nil {
		die("%s: %v", op, err)
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		die("%s failed: %s: %s", op, resp.Status, string(b))
	}
}

func nz(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
