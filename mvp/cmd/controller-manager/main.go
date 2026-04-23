// mk-controller-manager runs the ReplicaSet and Deployment reconciliation
// loops in a single process, analogous to kube-controller-manager.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/controllers"
)

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	interval := flag.Duration("interval", 2*time.Second, "reconcile interval")
	flag.Parse()

	c := client.New(*apiAddr)
	stop := make(chan struct{})

	rc := controllers.NewReplicaSetController(c, *interval)
	dc := controllers.NewDeploymentController(c, *interval)
	sc := controllers.NewServiceController(c, *interval)

	go rc.Run(stop)
	go dc.Run(stop)
	go sc.Run(stop)

	log.Printf("mk-controller-manager: api=%s interval=%s", *apiAddr, *interval)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	close(stop)
	log.Printf("mk-controller-manager: shutting down")
}
