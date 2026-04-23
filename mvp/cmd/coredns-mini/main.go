// mk-coredns-mini is a tiny embedded DNS server that resolves
// <service>.<namespace>.svc.cluster.local (and <service>.<namespace>)
// to the Service's ClusterIP. Backed by the apiserver.
package main

import (
	"flag"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

type cache struct {
	mu       sync.RWMutex
	services map[string]string // "name.namespace" -> clusterIP
}

func (c *cache) set(m map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services = m
}

func (c *cache) lookup(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ip, ok := c.services[key]
	return ip, ok
}

func main() {
	apiAddr := flag.String("api", "http://127.0.0.1:8080", "apiserver base URL")
	// 5353 is occupied on macOS (mDNSResponder). Default to 15353 which
	// is free on both macOS and Linux out of the box.
	listen := flag.String("listen", "127.0.0.1:15353", "address to listen on (UDP+TCP)")
	interval := flag.Duration("interval", 2*time.Second, "service sync interval")
	domain := flag.String("domain", "cluster.local.", "cluster DNS domain (trailing dot)")
	flag.Parse()

	c := &cache{services: map[string]string{}}
	cli := client.New(*apiAddr)

	go refreshLoop(cli, c, *interval)

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		for _, q := range r.Question {
			name := strings.ToLower(q.Name)
			// Match either <svc>.<ns>.svc.<domain> or <svc>.<ns>.<domain>.
			key := ""
			suffix1 := ".svc." + *domain
			suffix2 := "." + *domain
			switch {
			case strings.HasSuffix(name, suffix1):
				trim := strings.TrimSuffix(name, suffix1)
				parts := strings.Split(trim, ".")
				if len(parts) == 2 {
					key = parts[0] + "." + parts[1]
				}
			case strings.HasSuffix(name, suffix2):
				trim := strings.TrimSuffix(name, suffix2)
				parts := strings.Split(trim, ".")
				if len(parts) == 2 {
					key = parts[0] + "." + parts[1]
				}
			}
			if key == "" {
				continue
			}
			ip, ok := c.lookup(key)
			if !ok {
				continue
			}
			if q.Qtype != dns.TypeA && q.Qtype != dns.TypeANY {
				continue
			}
			rr := &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
				A:   net.ParseIP(ip),
			}
			m.Answer = append(m.Answer, rr)
			log.Printf("dns: %s -> %s", q.Name, ip)
		}
		_ = w.WriteMsg(m)
	})

	go func() {
		srv := &dns.Server{Addr: *listen, Net: "udp", Handler: handler}
		log.Printf("mk-coredns-mini: listening udp %s (domain=%s)", *listen, *domain)
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("dns udp: %v", err)
		}
	}()
	srv := &dns.Server{Addr: *listen, Net: "tcp", Handler: handler}
	log.Printf("mk-coredns-mini: listening tcp %s", *listen)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("dns tcp: %v", err)
	}
}

func refreshLoop(cli *client.Client, c *cache, interval time.Duration) {
	for {
		if err := refresh(cli, c); err != nil {
			log.Printf("dns refresh: %v", err)
		}
		time.Sleep(interval)
	}
}

func refresh(cli *client.Client, c *cache) error {
	svcs, err := cli.ListServices()
	if err != nil {
		return err
	}
	m := map[string]string{}
	for _, s := range svcs {
		if s.Spec.ClusterIP == "" {
			continue
		}
		ns := s.Metadata.Namespace
		if ns == "" {
			ns = "default"
		}
		m[s.Metadata.Name+"."+ns] = s.Spec.ClusterIP
	}
	c.set(m)
	return nil
}
