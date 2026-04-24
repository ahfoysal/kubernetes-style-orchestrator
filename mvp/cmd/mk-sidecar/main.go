// mk-sidecar is a tiny envoy-like sidecar proxy written in Go.
//
// It mirrors the core service-mesh idea: every pod gets one of these
// running next to the workload. All inter-pod traffic goes pod A ->
// sidecar A (client mode, plaintext -> mTLS) -> sidecar B (server mode,
// mTLS -> plaintext) -> workload B. Both endpoints present certificates
// signed by a shared test CA and verify each other (mutual TLS).
//
// Usage:
//
//	mk-sidecar -mode=server -listen=:9443 -upstream=127.0.0.1:8080 \
//	           -ca=ca.pem -cert=pod.pem -key=pod.key
//
//	mk-sidecar -mode=client -listen=127.0.0.1:8081 -upstream=remote:9443 \
//	           -ca=ca.pem -cert=pod.pem -key=pod.key -server-name=remote-pod
//
// If -ca/-cert/-key are omitted, the sidecar generates an in-memory
// ephemeral CA + leaf cert and writes them next to the binary so peer
// sidecars on the same host can reuse the same CA. This keeps the M6
// demo self-contained (no external PKI).
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	mode := flag.String("mode", "server", "server (terminate mTLS) | client (originate mTLS)")
	listen := flag.String("listen", ":9443", "local listen address")
	upstream := flag.String("upstream", "127.0.0.1:8080", "upstream address to forward to")
	caPath := flag.String("ca", "", "path to CA cert PEM; empty => auto-generate shared CA")
	certPath := flag.String("cert", "", "path to leaf cert PEM")
	keyPath := flag.String("key", "", "path to leaf private key PEM")
	serverName := flag.String("server-name", "mk-sidecar", "SAN / ServerName presented to peers")
	peerName := flag.String("peer-name", "mk-sidecar", "expected SAN/ServerName on the peer cert (client mode)")
	flag.Parse()

	caPEM, leafCert, leafKey, err := ensurePKI(*caPath, *certPath, *keyPath, *serverName)
	if err != nil {
		log.Fatalf("sidecar: pki: %v", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		log.Fatalf("sidecar: invalid CA PEM")
	}

	_ = leafKey // unused once baked into tls.Certificate

	switch *mode {
	case "server":
		runServer(*listen, *upstream, leafCert, caPool)
	case "client":
		runClient(*listen, *upstream, leafCert, caPool, *peerName)
	default:
		log.Fatalf("sidecar: unknown mode %q", *mode)
	}
}

// runServer terminates inbound mTLS and forwards plaintext to upstream.
func runServer(listen, upstream string, cert tls.Certificate, caPool *x509.CertPool) {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := tls.Listen("tcp", listen, cfg)
	if err != nil {
		log.Fatalf("sidecar server: listen %s: %v", listen, err)
	}
	log.Printf("mk-sidecar[server]: mTLS listen=%s upstream=%s", listen, upstream)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("sidecar server: accept: %v", err)
			continue
		}
		go func(client net.Conn) {
			defer client.Close()
			// Force TLS handshake so we log peer identity.
			if tc, ok := client.(*tls.Conn); ok {
				if err := tc.Handshake(); err != nil {
					log.Printf("sidecar server: handshake %s: %v", client.RemoteAddr(), err)
					return
				}
				peerSubject := "unknown"
				if cs := tc.ConnectionState(); len(cs.PeerCertificates) > 0 {
					peerSubject = cs.PeerCertificates[0].Subject.CommonName
				}
				log.Printf("sidecar server: mTLS OK peer=%s -> %s", peerSubject, upstream)
			}
			up, err := net.DialTimeout("tcp", upstream, 3*time.Second)
			if err != nil {
				log.Printf("sidecar server: upstream dial: %v", err)
				return
			}
			defer up.Close()
			biDir(client, up)
		}(c)
	}
}

// runClient accepts plaintext locally and forwards over mTLS to upstream.
func runClient(listen, upstream string, cert tls.Certificate, caPool *x509.CertPool, peerName string) {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ServerName:   peerName,
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("sidecar client: listen %s: %v", listen, err)
	}
	log.Printf("mk-sidecar[client]: listen=%s mTLS upstream=%s peer=%s", listen, upstream, peerName)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("sidecar client: accept: %v", err)
			continue
		}
		go func(client net.Conn) {
			defer client.Close()
			tc, err := tls.Dial("tcp", upstream, cfg)
			if err != nil {
				log.Printf("sidecar client: mTLS dial %s: %v", upstream, err)
				return
			}
			defer tc.Close()
			if err := tc.Handshake(); err != nil {
				log.Printf("sidecar client: handshake: %v", err)
				return
			}
			peerSubject := "unknown"
			if cs := tc.ConnectionState(); len(cs.PeerCertificates) > 0 {
				peerSubject = cs.PeerCertificates[0].Subject.CommonName
			}
			log.Printf("sidecar client: mTLS OK peer=%s", peerSubject)
			biDir(client, tc)
		}(c)
	}
}

func biDir(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// runServer wrapper to allow the signature to accept caPool.
// (Go lets us define inline below; separated to keep main readable.)

// ensurePKI loads CA + leaf cert or generates a self-signed CA and leaf
// pinned to the given SAN. Generated artifacts are written next to the
// binary (alongside /tmp/mk-sidecar-*.pem) so peer sidecars on the same
// host can reuse the same CA by pointing at the same paths.
func ensurePKI(caPath, certPath, keyPath, sanName string) (caPEM []byte, leaf tls.Certificate, _ *ecdsa.PrivateKey, err error) {
	if caPath != "" && certPath != "" && keyPath != "" {
		caPEM, err = os.ReadFile(caPath)
		if err != nil {
			return nil, tls.Certificate{}, nil, err
		}
		leaf, err = tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, tls.Certificate{}, nil, err
		}
		return caPEM, leaf, nil, nil
	}
	// Auto-generate + persist to /tmp so sibling sidecars share a CA.
	dir := os.TempDir()
	sharedCA := filepath.Join(dir, "mk-sidecar-ca.pem")
	sharedCAKey := filepath.Join(dir, "mk-sidecar-ca.key")
	caCert, caKey, err := loadOrCreateCA(sharedCA, sharedCAKey)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})

	// Always mint a fresh leaf so every sidecar instance has its own key.
	leafCert, leafKey, err := mintLeaf(caCert, caKey, sanName)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCert.Raw})
	keyBytes, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	tlsCert, err := tls.X509KeyPair(leafPEM, leafKeyPEM)
	if err != nil {
		return nil, tls.Certificate{}, nil, err
	}
	log.Printf("sidecar: auto-issued leaf cert CN=%s (shared CA=%s)", sanName, sharedCA)
	return caPEM, tlsCert, leafKey, nil
}

func loadOrCreateCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// Try load.
	if cBytes, err := os.ReadFile(certPath); err == nil {
		if kBytes, err := os.ReadFile(keyPath); err == nil {
			cBlock, _ := pem.Decode(cBytes)
			kBlock, _ := pem.Decode(kBytes)
			if cBlock != nil && kBlock != nil {
				cert, err1 := x509.ParseCertificate(cBlock.Bytes)
				key, err2 := x509.ParseECPrivateKey(kBlock.Bytes)
				if err1 == nil && err2 == nil {
					return cert, key, nil
				}
			}
		}
	}
	// Generate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "mk-sidecar-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	kb, _ := x509.MarshalECPrivateKey(key)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, key, nil
}

func mintLeaf(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{cn, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}
