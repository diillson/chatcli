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

// TestPostMortemStatusFieldParity is the GAP-07 regression guard for the
// PostMortem CRD. The 1.122.x ship put requiresHumanAction/requiredAction
// on Spec, which (a) violated the K8s convention for controller-derived
// facts and (b) was never persisted at runtime because the controller wrote
// them to Status while the CRD only declared them on Spec. The fields moved
// to Status — this test pins them there across all three CRD copies so the
// regression cannot silently come back.
func TestPostMortemStatusFieldParity(t *testing.T) {
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
			statusProps, err := readCRDPropertyMap(data, "status")
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, field := range wantFields {
				if _, ok := statusProps[field]; !ok {
					t.Fatalf(
						"PostMortemStatus field %q missing from CRD %s.\n"+
							"This is the GAP-07 regression: 1.122.x had the field on Spec but the controller writes Status.\n"+
							"Run `controller-gen crd ...` inside operator/ and copy the regenerated YAML to both Helm chart CRDs/ directories.",
						field, path,
					)
				}
			}
			// Defense in depth: also assert the fields are NOT on Spec, so a
			// stray hand-edit to the CRD doesn't reintroduce the duplicate.
			specProps, err := readCRDPropertyMap(data, "spec")
			if err != nil {
				t.Fatalf("parse %s (spec): %v", path, err)
			}
			for _, field := range wantFields {
				if _, ok := specProps[field]; ok {
					t.Fatalf(
						"PostMortemSpec must NOT declare %q anymore — it lives in Status (GAP-07). Found in %s",
						field, path,
					)
				}
			}
		})
	}
}

// TestIssueStatusHumanActionParity is the parity guard for the Issue CRD's
// human-action fields added by GAP-07. The Go IssueStatus has them; the CRD
// in every shipped copy must declare them so the API server lets the
// controller write `status.requiresHumanAction` and `status.requiredAction`.
func TestIssueStatusHumanActionParity(t *testing.T) {
	wantFields := []string{"requiredAction", "requiresHumanAction"}

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
			statusProps, err := readCRDPropertyMap(data, "status")
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, field := range wantFields {
				if _, ok := statusProps[field]; !ok {
					t.Fatalf(
						"IssueStatus field %q missing from CRD %s.\n"+
							"GAP-07 added these so `kubectl get issue -o jsonpath='{.status.%s}'` works for operators.",
						field, path, field,
					)
				}
			}
		})
	}
}

// readCRDPropertyMap returns the property names declared under the given
// subtree (spec or status) of the first version's openAPIV3Schema. Used by
// both the PostMortem and Issue parity tests so the YAML walking lives in
// one place.
func readCRDPropertyMap(data []byte, subtree string) (map[string]any, error) {
	var doc struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties map[string]struct {
							Properties map[string]any `yaml:"properties"`
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
	node, ok := doc.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties[subtree]
	if !ok {
		return nil, fmt.Errorf("CRD has no .%s subtree", subtree)
	}
	if len(node.Properties) == 0 {
		return nil, fmt.Errorf("CRD .%s.properties is empty", subtree)
	}
	return node.Properties, nil
}
