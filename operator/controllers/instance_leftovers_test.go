/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// maxRetries 0 is an explicit "fail over at once" (the CRD defaults an
// omitted value to 2) and must reach the server instead of its default.
func TestReconcileConfigMap_FallbackMaxRetriesZeroIsKept(t *testing.T) {
	for _, retries := range []int32{0, 3} {
		inst := newInstance("fb", "default")
		inst.Spec.Fallback = &platformv1alpha1.FallbackSpec{
			Enabled:    true,
			Providers:  []platformv1alpha1.FallbackProviderEntry{{Name: "OPENAI"}},
			MaxRetries: retries,
		}
		r, c := setupFakeReconciler(inst)
		if err := r.reconcileConfigMap(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
		var cm corev1.ConfigMap
		if err := c.Get(context.Background(), types.NamespacedName{Name: "fb", Namespace: "default"}, &cm); err != nil {
			t.Fatal(err)
		}
		if got, ok := cm.Data["CHATCLI_FALLBACK_MAX_RETRIES"]; !ok || got != map[int32]string{0: "0", 3: "3"}[retries] {
			t.Errorf("maxRetries %d: CHATCLI_FALLBACK_MAX_RETRIES = %q (present %v)", retries, got, ok)
		}
		// The providers list is the switch; the server reads no enable flag.
		if _, ok := cm.Data["CHATCLI_FALLBACK_ENABLED"]; ok || cm.Data["CHATCLI_FALLBACK_PROVIDERS"] != "CLAUDEAI,OPENAI" {
			t.Errorf("fallback env = %v", cm.Data)
		}
	}
}

func configMapFixture(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: data}
}

func updateConfigMapData(t *testing.T, c client.Client, name, key, value string) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[key] = value
	if err := c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
}

// Editing a ConfigMap the pod mounts as files (MCP servers, agents,
// skills) rolls the pods: the server reads them only at startup.
func TestReconcileDeployment_MountedConfigMapEditsRollThePods(t *testing.T) {
	inst := newInstance("mounts", "default")
	inst.Spec.MCP = &platformv1alpha1.MCPSpec{Enabled: true, ExistingConfigMap: "team-mcp"}
	inst.Spec.Agents = &platformv1alpha1.AgentProvisionSpec{ConfigMapRef: strPtr("team-agents"), SkillsConfigMapRef: strPtr("team-skills")}
	r, c := setupFakeReconciler(inst,
		configMapFixture("team-mcp", map[string]string{"mcp_servers.json": `{"servers":[]}`}),
		configMapFixture("team-agents", map[string]string{"reviewer.md": "v1"}),
		configMapFixture("team-skills", map[string]string{"triage.md": "v1"}),
	)
	ctx := context.Background()
	hash := func() string {
		t.Helper()
		if err := r.reconcileDeployment(ctx, inst); err != nil {
			t.Fatal(err)
		}
		var d appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Name: "mounts", Namespace: "default"}, &d); err != nil {
			t.Fatal(err)
		}
		return d.Spec.Template.Annotations[mountedConfigMapsHashAnnotation]
	}

	first := hash()
	if first == "" {
		t.Fatal("mounted ConfigMaps hash annotation missing")
	}
	if again := hash(); again != first {
		t.Fatalf("hash moved without a change: %s -> %s", first, again)
	}
	seen := map[string]bool{first: true}
	for _, edit := range [][3]string{
		{"team-mcp", "mcp_servers.json", `{"servers":[{"name":"git"}]}`},
		{"team-agents", "reviewer.md", "v2"},
		{"team-skills", "triage.md", "v2"},
	} {
		updateConfigMapData(t, c, edit[0], edit[1], edit[2])
		got := hash()
		if seen[got] {
			t.Fatalf("editing %s did not change the pod template", edit[0])
		}
		seen[got] = true
	}
}

// The operator-rendered <name>-mcp ConfigMap is hashed too.
func TestMountedConfigMapNames(t *testing.T) {
	inst := newInstance("srv", "default")
	if got := mountedConfigMapNames(inst); len(got) != 0 {
		t.Fatalf("nothing mounted: %v", got)
	}
	inst.Spec.MCP = &platformv1alpha1.MCPSpec{Enabled: true}
	inst.Spec.Agents = &platformv1alpha1.AgentProvisionSpec{SkillsConfigMapRef: strPtr("sk")}
	if got := strings.Join(mountedConfigMapNames(inst), ","); got != "srv-mcp,sk" {
		t.Fatalf("names = %s", got)
	}
}

// A user-managed ConfigMap event reaches the Instances that mount it, and
// only them.
func TestConfigMapToInstance(t *testing.T) {
	a := newInstance("a", "default")
	a.Spec.MCP = &platformv1alpha1.MCPSpec{Enabled: true, ExistingConfigMap: "shared-mcp"}
	b := newInstance("b", "default")
	b.UID = "uid-b"
	b.Spec.Agents = &platformv1alpha1.AgentProvisionSpec{ConfigMapRef: strPtr("shared-mcp")}
	other := newInstance("other", "default")
	other.UID = "uid-other"
	r, _ := setupFakeReconciler(a, b, other)

	reqs := r.configMapToInstance(context.Background(), configMapFixture("shared-mcp", nil))
	var names []string
	for _, q := range reqs {
		names = append(names, q.Name)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("requests = %v", names)
	}
	if reqs := r.configMapToInstance(context.Background(), configMapFixture("unrelated", nil)); len(reqs) != 0 {
		t.Fatalf("unrelated ConfigMap mapped to %v", reqs)
	}
}

// The sessions PVC is ReadWriteOnce: with persistence the Deployment
// replaces pods with Recreate so a surge pod never waits on Multi-Attach;
// without it the default rolling update stays.
func TestReconcileDeployment_StrategyFollowsPersistence(t *testing.T) {
	inst := newInstance("strategy", "default")
	inst.Spec.Persistence = &platformv1alpha1.PersistenceSpec{Enabled: true}
	r, c := setupFakeReconciler(inst)
	ctx := context.Background()
	get := func() appsv1.DeploymentStrategy {
		t.Helper()
		if err := r.reconcileDeployment(ctx, inst); err != nil {
			t.Fatal(err)
		}
		var d appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Name: "strategy", Namespace: "default"}, &d); err != nil {
			t.Fatal(err)
		}
		return d.Spec.Strategy
	}
	if s := get(); s.Type != appsv1.RecreateDeploymentStrategyType || s.RollingUpdate != nil {
		t.Fatalf("persistence: strategy = %+v", s)
	}
	inst.Spec.Persistence.Enabled = false
	if s := get(); s.Type != "" && s.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Fatalf("no persistence: strategy = %+v", s)
	}
}

// The default pod security context makes volumes group-writable for the
// server user; a user-supplied context is used as given.
func TestBuildPodSpec_DefaultFSGroup(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("fs", "default")
	sc := r.buildPodSpec(inst).SecurityContext
	if sc == nil || sc.FSGroup == nil || *sc.FSGroup != 1000 || sc.RunAsUser == nil || *sc.RunAsUser != *sc.FSGroup {
		t.Fatalf("default security context = %+v", sc)
	}
	if sc.FSGroupChangePolicy == nil || *sc.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Errorf("fsGroupChangePolicy = %v", sc.FSGroupChangePolicy)
	}

	custom := &corev1.PodSecurityContext{RunAsUser: int64Ptr(2000)}
	inst.Spec.SecurityContext = custom
	if got := r.buildPodSpec(inst).SecurityContext; got != custom || got.FSGroup != nil {
		t.Fatalf("custom security context changed: %+v", got)
	}
}

func rulesGrant(rules []rbacv1.PolicyRule, group, resource string, verbs ...string) bool {
	for _, verb := range verbs {
		granted := false
		for _, r := range rules {
			if contains(r.APIGroups, group) && contains(r.Resources, resource) && (contains(r.Verbs, verb) || contains(r.Verbs, "*")) {
				granted = true
				break
			}
		}
		if !granted {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The namespaced watcher Role reads Job and CronJob targets.
func TestWatcherPolicyRules_ReadJobsAndCronJobs(t *testing.T) {
	for _, res := range []string{"jobs", "cronjobs"} {
		if !rulesGrant(watcherPolicyRules(), "batch", res, "get", "list", "watch") {
			t.Errorf("watcher Role cannot read batch/%s", res)
		}
	}
}

// applyManifestKinds maps every kind the ApplyManifest allowlist admits to
// the API group and resource the operator must be allowed to write.
var applyManifestKinds = map[string][2]string{
	"Deployment":              {"apps", "deployments"},
	"StatefulSet":             {"apps", "statefulsets"},
	"DaemonSet":               {"apps", "daemonsets"},
	"Service":                 {"", "services"},
	"ConfigMap":               {"", "configmaps"},
	"HorizontalPodAutoscaler": {"autoscaling", "horizontalpodautoscalers"},
	"PodDisruptionBudget":     {"policy", "poddisruptionbudgets"},
	"Ingress":                 {"networking.k8s.io", "ingresses"},
	"CronJob":                 {"batch", "cronjobs"},
	"Job":                     {"batch", "jobs"},
	"ServiceMonitor":          {"monitoring.coreos.com", "servicemonitors"},
	"PrometheusRule":          {"monitoring.coreos.com", "prometheusrules"},
	"PodMonitor":              {"monitoring.coreos.com", "podmonitors"},
	"ServiceEntry":            {"networking.istio.io", "serviceentries"},
	"VirtualService":          {"networking.istio.io", "virtualservices"},
	"DestinationRule":         {"networking.istio.io", "destinationrules"},
}

var templateExpr = regexp.MustCompile(`\{\{-?[^}]*-?\}\}`)

// loadClusterRoles decodes the ClusterRoles of a manifest; Helm template
// expressions are blanked first, which leaves the rules untouched.
func loadClusterRoles(t *testing.T, path string) []rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed repository paths
	if err != nil {
		t.Fatal(err)
	}
	var roles []rbacv1.ClusterRole
	for _, doc := range strings.Split(string(raw), "\n---") {
		var lines []string
		for _, line := range strings.Split(doc, "\n") {
			if templateExpr.MatchString(line) && strings.TrimSpace(templateExpr.ReplaceAllString(line, "")) == "" {
				continue // a line that is only a template directive
			}
			lines = append(lines, templateExpr.ReplaceAllString(line, "tmpl"))
		}
		// YAML to JSON first: the API types carry json tags only.
		var generic map[string]interface{}
		if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &generic); err != nil || generic["kind"] != "ClusterRole" {
			continue
		}
		asJSON, err := json.Marshal(generic)
		if err != nil {
			t.Fatal(err)
		}
		var cr rbacv1.ClusterRole
		if err := json.Unmarshal(asJSON, &cr); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, cr)
	}
	return roles
}

func findRole(roles []rbacv1.ClusterRole, match func(rbacv1.ClusterRole) bool) *rbacv1.ClusterRole {
	for i := range roles {
		if match(roles[i]) {
			return &roles[i]
		}
	}
	return nil
}

// Every kind ApplyManifest may apply can be read, created and updated by
// the operator, in both the Helm chart and the kustomize manifests, and
// the shared chatcli-watcher ClusterRole reads Job and CronJob targets.
func TestOperatorRBAC_CoversApplyManifestAndWatcherTargets(t *testing.T) {
	for kind := range DefaultAllowedResourceTypes() {
		if _, ok := applyManifestKinds[kind]; !ok {
			t.Fatalf("allowlist kind %s has no RBAC mapping in this test; add it and grant it", kind)
		}
	}
	for path, isOperator := range map[string]func(rbacv1.ClusterRole) bool{
		"../config/rbac/role.yaml": func(cr rbacv1.ClusterRole) bool { return cr.Name == "chatcli-operator-manager" },
		"../../deploy/helm/chatcli-operator/templates/rbac.yaml": func(cr rbacv1.ClusterRole) bool {
			return cr.Labels["app.kubernetes.io/component"] == "controller"
		},
	} {
		roles := loadClusterRoles(t, path)
		op := findRole(roles, isOperator)
		if op == nil {
			t.Fatalf("%s: operator ClusterRole not found", path)
		}
		for kind := range DefaultAllowedResourceTypes() {
			gr := applyManifestKinds[kind]
			if !rulesGrant(op.Rules, gr[0], gr[1], "get", "create", "update") {
				t.Errorf("%s: ApplyManifest allows %s but the operator cannot get/create/update %s.%s", path, kind, gr[1], gr[0])
			}
		}
		watcher := findRole(roles, func(cr rbacv1.ClusterRole) bool { return cr.Name == SharedWatcherClusterRole })
		if watcher == nil {
			t.Fatalf("%s: %s ClusterRole not found", path, SharedWatcherClusterRole)
		}
		for _, res := range []string{"jobs", "cronjobs"} {
			if !rulesGrant(watcher.Rules, "batch", res, "get", "list", "watch") {
				t.Errorf("%s: %s cannot read batch/%s", path, SharedWatcherClusterRole, res)
			}
		}
		// The namespaced watcher Role the operator creates must hold no
		// permission the operator lacks (RBAC escalation check).
		for _, rule := range watcherPolicyRules() {
			for _, g := range rule.APIGroups {
				for _, res := range rule.Resources {
					if !rulesGrant(op.Rules, g, res, rule.Verbs...) {
						t.Errorf("%s: operator cannot grant %v on %s.%s to the watcher Role", path, rule.Verbs, res, g)
					}
				}
			}
		}
	}
}
