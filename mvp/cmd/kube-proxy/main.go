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
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/netpol"
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
	cli      *client.Client
	mu       sync.Mutex
	entries  map[proxyKey]*proxyEntry
	// Cached cluster view refreshed each sync tick. NetPol enforcement
	// reads these under atomic loads so the accept path never blocks.
	policies atomic.Value // []*api.NetworkPolicy
	pods     atomic.Value // []*api.Pod
	// netpolEnforce toggles whether policy denials actually drop traffic.
	netpolEnforce bool
}

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	interval := flag.Duration("interval", 2*time.Second, "sync interval")
	netpolEnforce := flag.Bool("netpol", true, "enforce NetworkPolicy (drop disallowed pod->pod connections)")
	flag.Parse()

	p := &proxy{
		cli:           client.New(*apiAddr),
		entries:       map[proxyKey]*proxyEntry{},
		netpolEnforce: *netpolEnforce,
	}
	p.policies.Store([]*api.NetworkPolicy{})
	p.pods.Store([]*api.Pod{})

	log.Printf("mk-kube-proxy: api=%s interval=%s netpol=%v", *apiAddr, *interval, *netpolEnforce)
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
	// Refresh NetworkPolicy + Pod cache; tolerate transient apiserver
	// hiccups (empty list is "no policies" = allow-all).
	if pols, err := p.cli.ListNetworkPolicies(); err == nil {
		p.policies.Store(pols)
	}
	if pods, err := p.cli.ListPods(); err == nil {
		p.pods.Store(pods)
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
				go p.acceptLoop(entry, svc.Metadata.Name)
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

func (p *proxy) acceptLoop(e *proxyEntry, svcName string) {
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			log.Printf("kube-proxy: service=%s accept: %v", svcName, err)
			return
		}
		go p.handle(e, svcName, conn)
	}
}

func (p *proxy) handle(e *proxyEntry, svcName string, client net.Conn) {
	defer client.Close()
	subs, _ := e.backends.Load().([]api.EndpointAddress)
	if len(subs) == 0 {
		log.Printf("kube-proxy: service=%s no endpoints, dropping conn", svcName)
		return
	}
	// Round-robin using atomic counter.
	idx := int(atomic.AddUint64(&e.counter, 1)-1) % len(subs)
	ep := subs[idx]

	// ---- NetworkPolicy enforcement ----
	// Map caller's source port back to a pod (stub runtime publishes a
	// unique loopback port per pod so this is a reliable pod identifier
	// within the MVP). Map the destination endpoint port back to the dst
	// pod likewise, then ask the evaluator.
	if p.netpolEnforce {
		policies, _ := p.policies.Load().([]*api.NetworkPolicy)
		pods, _ := p.pods.Load().([]*api.Pod)
		srcPort := 0
		if ta, ok := client.RemoteAddr().(*net.TCPAddr); ok {
			srcPort = ta.Port
		}
		srcPod := netpol.PodByHostPort(pods, srcPort)
		dstPod := findPodByName(pods, ep.PodName)
		dstContainerPort := ep.Port
		if dstPod != nil && len(dstPod.Spec.Containers) > 0 && len(dstPod.Spec.Containers[0].Ports) > 0 {
			dstContainerPort = dstPod.Spec.Containers[0].Ports[0].ContainerPort
		}
		dec := netpol.Evaluate(policies, srcPod, dstPod, dstContainerPort)
		if !dec.Allowed {
			log.Printf("kube-proxy: service=%s NETPOL DENY src=%s dst=pod/%s policy=%s reason=%s",
				svcName, podLabel(srcPod), ep.PodName, dec.Policy, dec.Reason)
			return
		}
	}

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

// findPodByName returns the pod with the given name (or nil).
func findPodByName(pods []*api.Pod, name string) *api.Pod {
	for _, pod := range pods {
		if pod.Metadata.Name == name {
			return pod
		}
	}
	return nil
}

// podLabel prints a short src-pod identifier for logs.
func podLabel(p *api.Pod) string {
	if p == nil {
		return "external"
	}
	return "pod/" + p.Metadata.Name
}
