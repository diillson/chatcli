/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package v1alpha1

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestIssueStateEnumParity is the GAP-06 regression guard.
//
// GAP-06 (chaos test report 2026-05-23): the 1.122.0 chart shipped the new
// IssueState `Contained` constant in the controller binary but the runtime
// CRD enum on cluster only listed the 1.121.0 values, so the API server
// rejected every Issue update with "Unsupported value: Contained". Floor 11
// catches drift between Go types and the regenerated YAML; this test catches
// the harder-to-find drift between the IssueState Go enum and what each of
// the THREE checked-in CRD YAML copies actually validates.
//
// Failure here means: a developer added an IssueState constant in Go and
// forgot to either (a) regenerate the CRDs with controller-gen, or (b)
// sync the regenerated CRD to the two Helm chart copies. Without this
// test the regression only surfaces in a live cluster.
func TestIssueStateEnumParity(t *testing.T) {
	// Source of truth: the Go IssueState constants declared in this file.
	wantStates := sortedStrings(map[string]struct{}{
		string(IssueStateDetected):    {},
		string(IssueStateAnalyzing):   {},
		string(IssueStateRemediating): {},
		string(IssueStateContained):   {},
		string(IssueStateResolved):    {},
		string(IssueStateEscalated):   {},
		string(IssueStateFailed):      {},
	})

	// Every CRD copy that ships with the project. Adding a new chart that
	// vendors a CRD copy requires adding the path here AND updating the
	// scripts/qg/crd-drift.sh Floor 11 extension.
	crdPaths := []string{
		"../../config/crd/bases/platform.chatcli.io_issues.yaml",
		"../../../deploy/helm/chatcli-operator/crds/platform.chatcli.io_issues.yaml",
		"../../../deploy/helm/chatcli/crds/platform.chatcli.io_issues.yaml",
	}

	for _, rel := range crdPaths {
		t.Run(filepath.Base(filepath.Dir(rel))+"/"+filepath.Base(rel), func(t *testing.T) {
			path := filepath.Clean(rel)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			gotStates, err := readIssueStateEnumFromCRD(data)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			gotSorted := sortedStrings(toSet(gotStates))
			if !reflect.DeepEqual(gotSorted, wantStates) {
				t.Fatalf(
					"IssueState enum drift in %s:\n  CRD lists:  %v\n  Go declares: %v\n\n"+
						"Run `controller-gen crd ...` inside operator/ and copy the regenerated YAML "+
						"to deploy/helm/chatcli-operator/crds/ AND deploy/helm/chatcli/crds/ — see "+
						"GAP-06 (chaos test report 2026-05-23) for the regression this guards against.",
					path, gotSorted, wantStates,
				)
			}
		})
	}
}

// readIssueStateEnumFromCRD parses the multi-document CRD YAML and returns
// the enum values declared on the .status.state field. The CRD schema is
// nested deep enough that we use a typed walk rather than a brittle string
// search — that way a structural rename surfaces as a parse failure here
// instead of a silent miss in the parity check.
func readIssueStateEnumFromCRD(data []byte) ([]string, error) {
	var doc struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Status struct {
								Properties struct {
									State struct {
										Enum []string `yaml:"enum"`
									} `yaml:"state"`
								} `yaml:"properties"`
							} `yaml:"status"`
						} `yaml:"properties"`
					} `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if len(doc.Spec.Versions) == 0 {
		return nil, fmt.Errorf("CRD has no versions declared")
	}
	enum := doc.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Status.Properties.State.Enum
	if len(enum) == 0 {
		return nil, fmt.Errorf(".spec.versions[0].schema.openAPIV3Schema.properties.status.properties.state.enum is empty — CRD structure may have changed")
	}
	return enum, nil
}

// toSet builds a deduplicated string set from a slice.
func toSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, v := range in {
		out[strings.TrimSpace(v)] = struct{}{}
	}
	return out
}

// sortedStrings turns a set into a deterministic sorted slice for comparison.
func sortedStrings(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPostMortemSpecFieldParity is the GAP-06 regression guard for the
// PostMortem CRD. Mirrors TestIssueStateEnumParity: the controller writes
// spec.requiresHumanAction and spec.requiredAction (added in 1.122.0) and
// the runtime API server must accept them — otherwise generatePostMortem
// fails every time an Issue goes Contained.
func TestPostMortemSpecFieldParity(t *testing.T) {
	wantFields := []string{"requiredAction", "requiresHumanAction"}

	crdPaths := []string{
		"../../config/crd/bases/platform.chatcli.io_postmortems.yaml",
		"../../../deploy/helm/chatcli-operator/crds/platform.chatcli.io_postmortems.yaml",
		"../../../deploy/helm/chatcli/crds/platform.chatcli.io_postmortems.yaml",
	}

	for _, rel := range crdPaths {
		t.Run(filepath.Base(filepath.Dir(rel))+"/"+filepath.Base(rel), func(t *testing.T) {
			path := filepath.Clean(rel)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			specProps, err := readPostMortemSpecProperties(data)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, field := range wantFields {
				if _, ok := specProps[field]; !ok {
					t.Fatalf(
						"PostMortemSpec field %q missing from CRD %s.\n"+
							"This is the GAP-06 regression: 1.122.0 shipped the controller field but the CRD copy was stale.\n"+
							"Run `controller-gen crd ...` inside operator/ and copy the regenerated YAML to both Helm chart CRDs/ directories.",
						field, path,
					)
				}
			}
		})
	}
}

// readPostMortemSpecProperties returns the property names declared under
// .spec.versions[0].schema.openAPIV3Schema.properties.spec.properties — that
// is the per-CR spec fields the API server validates.
func readPostMortemSpecProperties(data []byte) (map[string]any, error) {
	var doc struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]any `yaml:"properties"`
							} `yaml:"spec"`
						} `yaml:"properties"`
					} `yaml:"openAPIV3Schema"`
				} `yaml:"schema"`
			} `yaml:"versions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if len(doc.Spec.Versions) == 0 {
		return nil, fmt.Errorf("CRD has no versions declared")
	}
	props := doc.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties
	if len(props) == 0 {
		return nil, fmt.Errorf(".spec.versions[0].schema.openAPIV3Schema.properties.spec.properties is empty")
	}
	return props, nil
}
