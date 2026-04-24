// Package crd contains helpers for the CustomResourceDefinition feature.
//
// CRs live under dynamic REST paths:
//
//   /apis/{group}/{version}/{plural}
//   /apis/{group}/{version}/{plural}/{name}
//
// For example, after applying a CRD with group=mk.io, version=v1,
// plural=databases, a user can POST/GET/DELETE /apis/mk.io/v1/databases.
package crd

import (
	"fmt"
	"strings"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// PathPrefix is the dynamic-CR URL namespace.
const PathPrefix = "/apis/"

// ParsePath splits a dynamic CR URL into (group, version, plural, name).
// Returns name="" for collection paths. Returns error if the path is not a
// dynamic CR path (e.g. apps/v1 built-ins).
func ParsePath(p string) (group, version, plural, name string, err error) {
	s := strings.TrimPrefix(p, PathPrefix)
	parts := strings.Split(s, "/")
	if len(parts) < 3 {
		return "", "", "", "", fmt.Errorf("not a CR path: %s", p)
	}
	group, version, plural = parts[0], parts[1], parts[2]
	if len(parts) >= 4 {
		name = parts[3]
	}
	return group, version, plural, name, nil
}

// ValidateSpec checks that every required property from the CRD schema is
// present in spec and that scalar types line up loosely (string/number/
// boolean/object/array). Unknown properties are tolerated.
func ValidateSpec(schema api.CRDSchema, spec map[string]interface{}) error {
	for _, req := range schema.Required {
		if _, ok := spec[req]; !ok {
			return fmt.Errorf("spec.%s is required", req)
		}
	}
	for k, prop := range schema.Properties {
		v, ok := spec[k]
		if !ok {
			continue
		}
		if prop.Type == "" {
			continue
		}
		if !typeMatches(prop.Type, v) {
			return fmt.Errorf("spec.%s: expected %s, got %T", k, prop.Type, v)
		}
	}
	return nil
}

func typeMatches(t string, v interface{}) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "integer", "number":
		switch v.(type) {
		case int, int32, int64, float32, float64:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]interface{})
		return ok
	case "array":
		_, ok := v.([]interface{})
		return ok
	}
	return true
}
