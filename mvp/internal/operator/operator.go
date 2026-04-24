// Package operator is a tiny framework for writing controllers against
// CustomResources, analogous to operator-sdk / controller-runtime.
//
// Authors implement a Reconciler:
//
//	type MyReconciler struct{}
//	func (r *MyReconciler) Reconcile(ctx Context, cr *api.CustomResource) error { ... }
//
// then wire it into a Manager that polls the apiserver on an interval
// and calls Reconcile for every CR of the registered CRD.
package operator

import (
	"context"
	"log"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/client"
)

// Context is passed to Reconcile. It carries a typed apiserver client and
// a logger prefixed with the operator name + CR name.
type Context struct {
	Ctx    context.Context
	Client *client.Client
	Log    *log.Logger
}

// Reconciler drives a single CR type toward its desired state.
type Reconciler interface {
	Reconcile(ctx Context, cr *api.CustomResource) error
}

// Manager runs one or more Reconcilers on a polling interval.
type Manager struct {
	Name     string
	Client   *client.Client
	Interval time.Duration
	// CRDName is the CRD's object name (e.g. "databases.mk.io"). Used for
	// listing CRs out of the apiserver.
	CRDName    string
	Reconciler Reconciler
}

func (m *Manager) Run(stop <-chan struct{}) {
	log.Printf("%s: starting (crd=%s interval=%s)", m.Name, m.CRDName, m.Interval)
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.reconcileAll()
		}
	}
}

func (m *Manager) reconcileAll() {
	crs, err := m.Client.ListCustomResources(m.CRDName)
	if err != nil {
		log.Printf("%s: list CRs failed: %v", m.Name, err)
		return
	}
	for _, cr := range crs {
		lg := log.New(log.Writer(), m.Name+" cr="+cr.Metadata.Name+": ", log.LstdFlags)
		if err := m.Reconciler.Reconcile(Context{Ctx: context.Background(), Client: m.Client, Log: lg}, cr); err != nil {
			lg.Printf("reconcile error: %v", err)
		}
	}
}
