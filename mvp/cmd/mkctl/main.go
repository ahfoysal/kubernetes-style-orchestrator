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
	default:
		die("unsupported kind %q", peek.Kind)
	}
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
	default:
		die("unknown resource %q", args[0])
	}
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
