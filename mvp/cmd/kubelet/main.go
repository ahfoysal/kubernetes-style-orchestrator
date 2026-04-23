package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	nodeName := flag.String("node", "node-1", "this node's name")
	interval := flag.Duration("interval", 2*time.Second, "poll interval")
	useDocker := flag.Bool("docker", true, "use docker run; if false or docker missing, falls back to echo stub")
	flag.Parse()

	docker := *useDocker && dockerAvailable()
	if !docker {
		log.Printf("mk-kubelet: docker unavailable or disabled; using echo-stub runtime")
	}
	log.Printf("mk-kubelet: api=%s node=%s interval=%s docker=%v", *apiAddr, *nodeName, *interval, docker)
	for {
		if err := syncOnce(*apiAddr, *nodeName, docker); err != nil {
			log.Printf("sync error: %v", err)
		}
		time.Sleep(*interval)
	}
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
	for _, p := range list.Items {
		switch p.Status.Phase {
		case api.PhaseScheduled:
			runPod(apiAddr, p, useDocker)
		}
	}
	return nil
}

func runPod(apiAddr string, p *api.Pod, useDocker bool) {
	if len(p.Spec.Containers) == 0 {
		patch(apiAddr, p.Metadata.Name, api.PodStatus{Phase: api.PhaseFailed, NodeName: p.Status.NodeName, Message: "no containers"})
		return
	}
	c := p.Spec.Containers[0]
	containerID := ""
	var runErr error
	if useDocker {
		containerID, runErr = dockerRun(p.Metadata.Name, c)
	} else {
		containerID, runErr = stubRun(p.Metadata.Name, c)
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
	})
	log.Printf("pod=%s running (containerID=%s)", p.Metadata.Name, containerID)
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

func stubRun(podName string, c api.Container) (string, error) {
	// echo-based stub: just log what would have run
	msg := fmt.Sprintf("[stub-run] pod=%s image=%s cmd=%v args=%v", podName, c.Image, c.Command, c.Args)
	cmd := exec.Command("echo", msg)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return "stub-" + podName, nil
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
