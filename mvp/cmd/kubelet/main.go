package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// stubServers tracks running in-process HTTP listeners for stub-run pods
// so we can shut them down when a pod disappears from the apiserver.
var (
	stubMu      sync.Mutex
	stubServers = map[string]*stubServer{}
)

type stubServer struct {
	ln       net.Listener
	shutdown func()
}

// mockCPUOverride, if set, forces a specific CPU % for the /metrics/pods
// endpoint. Used by the HPA demo to drive scale up/down.
var mockCPUOverride float64 = -1

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	nodeName := flag.String("node", "node-1", "this node's name")
	interval := flag.Duration("interval", 2*time.Second, "poll interval")
	useDocker := flag.Bool("docker", true, "use docker run; if false or docker missing, falls back to echo stub")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:10250", "listen address for /metrics/pods (for metrics-server to scrape)")
	mockCPU := flag.Float64("mock-cpu", -1, "if >=0, report this CPU%% for every pod (overrides the pseudo-random default)")
	flag.Parse()

	mockCPUOverride = *mockCPU
	docker := *useDocker && dockerAvailable()
	if !docker {
		log.Printf("mk-kubelet: docker unavailable or disabled; using echo-stub runtime")
	}

	// M5: metrics endpoint consumed by mk-metrics-server.
	go serveMetrics(*metricsAddr, *nodeName, *apiAddr)

	// M5: admin endpoint to drive mock CPU% at runtime, e.g.
	//   curl -X PUT http://127.0.0.1:10250/admin/cpu -d '80'
	log.Printf("mk-kubelet: api=%s node=%s interval=%s docker=%v metrics=%s", *apiAddr, *nodeName, *interval, docker, *metricsAddr)
	for {
		if err := syncOnce(*apiAddr, *nodeName, docker); err != nil {
			log.Printf("sync error: %v", err)
		}
		time.Sleep(*interval)
	}
}

// serveMetrics exposes /metrics/pods with per-pod CPU %. If mockCPUOverride
// is set, every pod reports that fixed percentage. Otherwise, each pod
// gets a pseudo-random 5–15 % baseline so tests are deterministic-ish.
//
// /admin/cpu accepts PUT with a numeric body to update mockCPUOverride at
// runtime — the demo script toggles this to drive scale-up and scale-down.
func serveMetrics(addr, nodeName, apiAddr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics/pods", func(w http.ResponseWriter, r *http.Request) {
		pods, err := listNodePods(apiAddr, nodeName)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		items := make([]api.PodMetric, 0, len(pods))
		for _, p := range pods {
			cpu := mockCPUOverride
			if cpu < 0 {
				// Pseudo-stable low baseline.
				cpu = 5 + float64(len(p.Metadata.Name)%10)
			}
			items = append(items, api.PodMetric{
				PodName: p.Metadata.Name, NodeName: nodeName, CPUPct: cpu,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
	})
	mux.HandleFunc("/admin/cpu", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			http.Error(w, "PUT or POST expected", 405)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%f", &v); err != nil {
			http.Error(w, "body must be a number: "+err.Error(), 400)
			return
		}
		mockCPUOverride = v
		log.Printf("mk-kubelet: mockCPU set to %.1f%%", v)
		fmt.Fprintf(w, "ok %.1f\n", v)
	})
	log.Printf("mk-kubelet: metrics endpoint on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("metrics listen: %v", err)
	}
}

func listNodePods(apiAddr, node string) ([]*api.Pod, error) {
	r, err := http.Get(apiAddr + "/api/v1/pods?nodeName=" + node)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("%s: %s", r.Status, string(b))
	}
	var out struct {
		Items []*api.Pod `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	// ensure daemon is reachable
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func syncOnce(apiAddr, node string, useDocker bool) error {
	url := apiAddr + "/api/v1/pods?nodeName=" + node
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("list: %s: %s", resp.Status, string(b))
	}
	var list struct {
		Items []*api.Pod `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range list.Items {
		seen[p.Metadata.Name] = true
		switch p.Status.Phase {
		case api.PhaseScheduled:
			runPod(apiAddr, p, useDocker)
		}
	}
	// Reap stub servers for pods that no longer exist.
	stubMu.Lock()
	for name, srv := range stubServers {
		if !seen[name] {
			srv.shutdown()
			delete(stubServers, name)
			log.Printf("pod=%s stub-runtime stopped (pod gone)", name)
		}
	}
	stubMu.Unlock()
	return nil
}

func runPod(apiAddr string, p *api.Pod, useDocker bool) {
	if len(p.Spec.Containers) == 0 {
		patch(apiAddr, p.Metadata.Name, api.PodStatus{Phase: api.PhaseFailed, NodeName: p.Status.NodeName, Message: "no containers"})
		return
	}
	c := p.Spec.Containers[0]
	containerID := ""
	podIP := ""
	hostPort := 0
	var runErr error
	if useDocker {
		containerID, runErr = dockerRun(p.Metadata.Name, c)
	} else {
		containerID, podIP, hostPort, runErr = stubRun(p.Metadata.Name, c)
	}
	if runErr != nil {
		patch(apiAddr, p.Metadata.Name, api.PodStatus{
			Phase: api.PhaseFailed, NodeName: p.Status.NodeName,
			ContainerID: containerID, Message: "run failed: " + runErr.Error(),
		})
		log.Printf("pod=%s FAILED: %v", p.Metadata.Name, runErr)
		return
	}
	patch(apiAddr, p.Metadata.Name, api.PodStatus{
		Phase: api.PhaseRunning, NodeName: p.Status.NodeName,
		ContainerID: containerID, Message: "started",
		PodIP: podIP, HostPort: hostPort,
	})
	log.Printf("pod=%s running (containerID=%s podIP=%s hostPort=%d)", p.Metadata.Name, containerID, podIP, hostPort)
}

func dockerRun(podName string, c api.Container) (string, error) {
	name := "mk-" + podName
	// remove any stale container with same name (idempotent)
	_ = exec.Command("docker", "rm", "-f", name).Run()
	args := []string{"run", "-d", "--name", name}
	args = append(args, c.Image)
	if len(c.Command) > 0 {
		args = append(args, c.Command...)
	}
	if len(c.Args) > 0 {
		args = append(args, c.Args...)
	}
	cmd := exec.Command("docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// stubRun starts an in-process HTTP server that responds with the pod
// name. This lets the kube-proxy/DNS demo round-robin across distinct
// "containers" without needing a real container runtime.
func stubRun(podName string, c api.Container) (string, string, int, error) {
	stubMu.Lock()
	if existing, ok := stubServers[podName]; ok {
		port := existing.ln.Addr().(*net.TCPAddr).Port
		stubMu.Unlock()
		return "stub-" + podName, "127.0.0.1", port, nil
	}
	stubMu.Unlock()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", 0, fmt.Errorf("stub listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from pod %s (image=%s port=%d)\n", podName, c.Image, port)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	// Echo to console so the stub runtime stays visibly "alive".
	_ = exec.Command("echo", fmt.Sprintf("[stub-run] pod=%s image=%s listening=127.0.0.1:%d", podName, c.Image, port)).Run()

	stubMu.Lock()
	stubServers[podName] = &stubServer{
		ln:       ln,
		shutdown: func() { _ = srv.Close() },
	}
	stubMu.Unlock()
	return "stub-" + podName, "127.0.0.1", port, nil
}

func patch(apiAddr, name string, s api.PodStatus) {
	body, _ := json.Marshal(s)
	req, _ := http.NewRequest(http.MethodPut, apiAddr+"/api/v1/pods/"+name+"/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("patch %s: %v", name, err)
		return
	}
	r.Body.Close()
}
