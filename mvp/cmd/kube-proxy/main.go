// mk-kube-proxy is a tiny userspace replacement for kube-proxy.
//
// It polls the apiserver for Services + Endpoints and, for each Service,
// binds a TCP listener on ClusterIP:port. Incoming connections are
// L4-forwarded to a live endpoint pod chosen by round-robin. When the
// Endpoints set changes, existing listeners keep running (no flapping)
// and new Services get fresh listeners.
//
// On macOS/Linux you will need an alias on the loopback interface so
// 127.20.0.X addresses are reachable:
//   sudo ifconfig lo0 alias 127.20.0.2 up     # mac
//   sudo ip addr add 127.20.0.2/8 dev lo      # linux
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

type proxyKey struct {
	service string
	port    int
}

type proxyEntry struct {
	key      proxyKey
	clusterIP string
	ln       net.Listener
	// backends is a pointer so we can atomically swap it without
	// locking the accept loop.
	backends atomic.Value // []api.EndpointAddress
	counter  uint64
}

type proxy struct {
	cli     *client.Client
	mu      sync.Mutex
	entries map[proxyKey]*proxyEntry
}

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	interval := flag.Duration("interval", 2*time.Second, "sync interval")
	flag.Parse()

	p := &proxy{
		cli:     client.New(*apiAddr),
		entries: map[proxyKey]*proxyEntry{},
	}

	log.Printf("mk-kube-proxy: api=%s interval=%s", *apiAddr, *interval)
	for {
		if err := p.sync(); err != nil {
			log.Printf("sync error: %v", err)
		}
		time.Sleep(*interval)
	}
}

func (p *proxy) sync() error {
	svcs, err := p.cli.ListServices()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	active := map[proxyKey]bool{}
	for _, svc := range svcs {
		if svc.Spec.ClusterIP == "" {
			continue
		}
		ep, err := p.cli.GetEndpoints(svc.Metadata.Name)
		if err != nil {
			// Endpoints may not exist yet — treat as empty.
			ep = &api.Endpoints{}
		}
		for _, sp := range svc.Spec.Ports {
			key := proxyKey{service: svc.Metadata.Name, port: sp.Port}
			active[key] = true
			entry, ok := p.entries[key]
			if !ok {
				ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, sp.Port))
				if err != nil {
					log.Printf("kube-proxy: service=%s bind %s:%d failed: %v (did you alias the IP on lo?)",
						svc.Metadata.Name, svc.Spec.ClusterIP, sp.Port, err)
					continue
				}
				entry = &proxyEntry{key: key, clusterIP: svc.Spec.ClusterIP, ln: ln}
				entry.backends.Store(ep.Subsets)
				p.entries[key] = entry
				go acceptLoop(entry, svc.Metadata.Name)
				log.Printf("kube-proxy: service=%s listening %s:%d backends=%d",
					svc.Metadata.Name, svc.Spec.ClusterIP, sp.Port, len(ep.Subsets))
			} else {
				entry.backends.Store(ep.Subsets)
			}
		}
	}
	// Remove listeners for services that disappeared.
	for key, entry := range p.entries {
		if !active[key] {
			_ = entry.ln.Close()
			delete(p.entries, key)
			log.Printf("kube-proxy: service=%s port=%d removed", key.service, key.port)
		}
	}
	return nil
}

func acceptLoop(e *proxyEntry, svcName string) {
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			log.Printf("kube-proxy: service=%s accept: %v", svcName, err)
			return
		}
		go handle(e, svcName, conn)
	}
}

func handle(e *proxyEntry, svcName string, client net.Conn) {
	defer client.Close()
	subs, _ := e.backends.Load().([]api.EndpointAddress)
	if len(subs) == 0 {
		log.Printf("kube-proxy: service=%s no endpoints, dropping conn", svcName)
		return
	}
	// Round-robin using atomic counter.
	idx := int(atomic.AddUint64(&e.counter, 1)-1) % len(subs)
	ep := subs[idx]
	upstream := fmt.Sprintf("%s:%d", ep.IP, ep.Port)
	backend, err := net.DialTimeout("tcp", upstream, 2*time.Second)
	if err != nil {
		log.Printf("kube-proxy: service=%s dial %s: %v", svcName, upstream, err)
		return
	}
	defer backend.Close()
	log.Printf("kube-proxy: service=%s %s -> pod=%s %s",
		svcName, client.RemoteAddr(), ep.PodName, upstream)

	// Bidirectional copy.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, backend); done <- struct{}{} }()
	<-done
}
