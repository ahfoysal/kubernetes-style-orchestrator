package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// EnsureM6Tables creates the M6 tables. Called by Open via a post-migrate
// hook; safe to call multiple times.
func (s *Store) EnsureM6Tables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS network_policies (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS clusters (
	name TEXT PRIMARY KEY,
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS federated_deployments (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
	}
	for _, s1 := range stmts {
		if _, err := s.db.Exec(s1); err != nil {
			return err
		}
	}
	return nil
}

// ---------------- NetworkPolicy ----------------

func (s *Store) UpsertNetworkPolicy(np *api.NetworkPolicy) error {
	now := time.Now().UTC()
	specB, _ := json.Marshal(np.Spec)
	metaB, _ := json.Marshal(np.Metadata)
	_, err := s.db.Exec(`INSERT INTO network_policies(name, namespace, spec_json, metadata_json, created_at, last_updated)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET namespace=excluded.namespace, spec_json=excluded.spec_json, metadata_json=excluded.metadata_json, last_updated=excluded.last_updated`,
		np.Metadata.Name, nsOrDefault(np.Metadata.Namespace), string(specB), string(metaB), now, now)
	return err
}

func (s *Store) GetNetworkPolicy(name string) (*api.NetworkPolicy, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json FROM network_policies WHERE name=?`, name)
	var specJ, metaJ string
	if err := row.Scan(&specJ, &metaJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	np := &api.NetworkPolicy{APIVersion: "networking.mk.io/v1", Kind: "NetworkPolicy"}
	_ = json.Unmarshal([]byte(specJ), &np.Spec)
	_ = json.Unmarshal([]byte(metaJ), &np.Metadata)
	return np, nil
}

func (s *Store) ListNetworkPolicies() ([]*api.NetworkPolicy, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json FROM network_policies ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.NetworkPolicy
	for rows.Next() {
		var specJ, metaJ string
		if err := rows.Scan(&specJ, &metaJ); err != nil {
			return nil, err
		}
		np := &api.NetworkPolicy{APIVersion: "networking.mk.io/v1", Kind: "NetworkPolicy"}
		_ = json.Unmarshal([]byte(specJ), &np.Spec)
		_ = json.Unmarshal([]byte(metaJ), &np.Metadata)
		out = append(out, np)
	}
	return out, rows.Err()
}

func (s *Store) DeleteNetworkPolicy(name string) error {
	res, err := s.db.Exec(`DELETE FROM network_policies WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Clusters ----------------

func (s *Store) UpsertCluster(c *api.Cluster) error {
	now := time.Now().UTC()
	specB, _ := json.Marshal(c.Spec)
	metaB, _ := json.Marshal(c.Metadata)
	statusB, _ := json.Marshal(c.Status)
	_, err := s.db.Exec(`INSERT INTO clusters(name, spec_json, metadata_json, status_json, created_at, last_updated)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET spec_json=excluded.spec_json, metadata_json=excluded.metadata_json, status_json=excluded.status_json, last_updated=excluded.last_updated`,
		c.Metadata.Name, string(specB), string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetCluster(name string) (*api.Cluster, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM clusters WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c := &api.Cluster{APIVersion: "federation.mk.io/v1", Kind: "Cluster"}
	_ = json.Unmarshal([]byte(specJ), &c.Spec)
	_ = json.Unmarshal([]byte(metaJ), &c.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &c.Status)
	return c, nil
}

func (s *Store) ListClusters() ([]*api.Cluster, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM clusters ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Cluster
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		c := &api.Cluster{APIVersion: "federation.mk.io/v1", Kind: "Cluster"}
		_ = json.Unmarshal([]byte(specJ), &c.Spec)
		_ = json.Unmarshal([]byte(metaJ), &c.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &c.Status)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateClusterStatus(name string, st api.ClusterStatus) error {
	st.LastHeartbeat = time.Now().UTC()
	b, _ := json.Marshal(st)
	res, err := s.db.Exec(`UPDATE clusters SET status_json=?, last_updated=? WHERE name=?`, string(b), st.LastHeartbeat, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteCluster(name string) error {
	res, err := s.db.Exec(`DELETE FROM clusters WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- FederatedDeployment ----------------

func (s *Store) UpsertFederatedDeployment(fd *api.FederatedDeployment) error {
	now := time.Now().UTC()
	existing, err := s.GetFederatedDeployment(fd.Metadata.Name)
	if err == nil {
		fd.Status = existing.Status
	}
	specB, _ := json.Marshal(fd.Spec)
	metaB, _ := json.Marshal(fd.Metadata)
	statusB, _ := json.Marshal(fd.Status)
	_, err = s.db.Exec(`INSERT INTO federated_deployments(name, namespace, spec_json, metadata_json, status_json, created_at, last_updated)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET namespace=excluded.namespace, spec_json=excluded.spec_json, metadata_json=excluded.metadata_json, status_json=excluded.status_json, last_updated=excluded.last_updated`,
		fd.Metadata.Name, nsOrDefault(fd.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetFederatedDeployment(name string) (*api.FederatedDeployment, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM federated_deployments WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	fd := &api.FederatedDeployment{APIVersion: "federation.mk.io/v1", Kind: "FederatedDeployment"}
	_ = json.Unmarshal([]byte(specJ), &fd.Spec)
	_ = json.Unmarshal([]byte(metaJ), &fd.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &fd.Status)
	return fd, nil
}

func (s *Store) ListFederatedDeployments() ([]*api.FederatedDeployment, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM federated_deployments ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.FederatedDeployment
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		fd := &api.FederatedDeployment{APIVersion: "federation.mk.io/v1", Kind: "FederatedDeployment"}
		_ = json.Unmarshal([]byte(specJ), &fd.Spec)
		_ = json.Unmarshal([]byte(metaJ), &fd.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &fd.Status)
		out = append(out, fd)
	}
	return out, rows.Err()
}

func (s *Store) UpdateFederatedDeploymentStatus(name string, st api.FederatedDeploymentStatus) error {
	st.LastUpdated = time.Now().UTC()
	b, _ := json.Marshal(st)
	res, err := s.db.Exec(`UPDATE federated_deployments SET status_json=?, last_updated=? WHERE name=?`, string(b), st.LastUpdated, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteFederatedDeployment(name string) error {
	res, err := s.db.Exec(`DELETE FROM federated_deployments WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
