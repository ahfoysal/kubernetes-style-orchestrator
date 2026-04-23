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
  apply -f <file.yaml>     Create or update a Pod from YAML
  get pods                 List pods
  describe pod <name>      Show a pod (JSON)
  delete pod <name>        Delete a pod

Env:
  MK_SERVER                Default server URL
`)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
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
	if err != nil {
		die("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		die("apply failed: %s: %s", resp.Status, string(b))
	}
	var out api.Pod
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("pod/%s applied (phase=%s)\n", out.Metadata.Name, out.Status.Phase)
}

func getCmd(server string, args []string) {
	if len(args) < 1 || args[0] != "pods" {
		die("usage: mkctl get pods")
	}
	resp, err := http.Get(server + "/api/v1/pods")
	if err != nil {
		die("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		die("get pods failed: %s: %s", resp.Status, string(b))
	}
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

func describeCmd(server string, args []string) {
	if len(args) < 2 || args[0] != "pod" {
		die("usage: mkctl describe pod <name>")
	}
	name := args[1]
	resp, err := http.Get(server + "/api/v1/pods/" + name)
	if err != nil {
		die("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		die("describe failed: %s: %s", resp.Status, string(b))
	}
	var p api.Pod
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		die("decode: %v", err)
	}
	out, _ := json.MarshalIndent(p, "", "  ")
	fmt.Println(string(out))
}

func deleteCmd(server string, args []string) {
	if len(args) < 2 || args[0] != "pod" {
		die("usage: mkctl delete pod <name>")
	}
	name := args[1]
	req, _ := http.NewRequest(http.MethodDelete, server+"/api/v1/pods/"+name, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		die("delete failed: %s: %s", resp.Status, string(b))
	}
	fmt.Printf("pod/%s deleted\n", name)
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
