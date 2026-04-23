package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	nodeName := flag.String("node", "node-1", "only node to schedule pods onto")
	interval := flag.Duration("interval", 2*time.Second, "scheduling interval")
	flag.Parse()

	log.Printf("mk-scheduler: api=%s node=%s interval=%s", *apiAddr, *nodeName, *interval)
	for {
		if err := scheduleOnce(*apiAddr, *nodeName); err != nil {
			log.Printf("schedule error: %v", err)
		}
		time.Sleep(*interval)
	}
}

func scheduleOnce(apiAddr, node string) error {
	url := apiAddr + "/api/v1/pods?phase=Pending"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("list pods: %s: %s", resp.Status, string(b))
	}
	var list struct {
		Items []*api.Pod `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	for _, p := range list.Items {
		s := api.PodStatus{
			Phase:    api.PhaseScheduled,
			NodeName: node,
			Message:  "Scheduled by mk-scheduler",
		}
		body, _ := json.Marshal(s)
		req, _ := http.NewRequest(http.MethodPut, apiAddr+"/api/v1/pods/"+p.Metadata.Name+"/status", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r2, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("bind %s: %v", p.Metadata.Name, err)
			continue
		}
		r2.Body.Close()
		log.Printf("scheduled pod=%s -> node=%s", p.Metadata.Name, node)
	}
	return nil
}
