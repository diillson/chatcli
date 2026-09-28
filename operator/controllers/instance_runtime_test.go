/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func secretFixture(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// --- item 1 and 9: probes and the restricted init container ---

func TestBuildPodSpec_ServerProbesTargetHealthzOnTheMetricsPort(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("probed", "default")
	inst.Spec.Server.MetricsPort = 0 // unset still means 9090, never disabled

	c := r.buildPodSpec(inst).Containers[0]
	var metricsPort int32
	for _, p := range c.Ports {
		if p.Name == metricsPortName {
			metricsPort = p.ContainerPort
		}
	}
	if metricsPort != 9090 {
		t.Fatalf("named metrics port = %d, want 9090", metricsPort)
	}
	for name, p := range map[string]*corev1.Probe{"startup": c.StartupProbe, "readiness": c.ReadinessProbe, "liveness": c.LivenessProbe} {
		if p == nil || p.HTTPGet == nil {
			t.Fatalf("%s probe missing or not httpGet: %+v", name, p)
		}
		if p.HTTPGet.Path != "/healthz" || p.HTTPGet.Port.String() != metricsPortName || p.HTTPGet.Scheme != corev1.URISchemeHTTP {
			t.Errorf("%s probe = %+v, want GET /healthz on the metrics port", name, p.HTTPGet)
		}
	}
	// Startup is the generous one: minutes, not seconds, before liveness applies.
	if budget := c.StartupProbe.PeriodSeconds * c.StartupProbe.FailureThreshold; budget < 120 {
		t.Errorf("startup budget %ds is too tight for a slow start", budget)
	}
	if c.LivenessProbe.PeriodSeconds*c.LivenessProbe.FailureThreshold < 60 {
		t.Error("liveness must tolerate at least a minute of silence before restarting")
	}

	// A custom metrics port moves the named port the probes follow.
	inst.Spec.Server.MetricsPort = 9191
	c = r.buildPodSpec(inst).Containers[0]
	for _, p := range c.Ports {
		if p.Name == metricsPortName && p.ContainerPort != 9191 {
			t.Errorf("metrics port = %d, want 9191", p.ContainerPort)
		}
	}
}

func TestBuildPodSpec_PluginLoaderRunsRestricted(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("plugins", "default")
	inst.Spec.Plugins = &platformv1alpha1.PluginProvisionSpec{Image: "registry.example/plugins:1"}

	spec := r.buildPodSpec(inst)
	if len(spec.InitContainers) != 1 {
		t.Fatalf("want the plugin-loader init container, got %d", len(spec.InitContainers))
	}
	sc := spec.InitContainers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("plugin-loader security context = %+v, want the restricted one", sc)
	}
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot {
		t.Error("the pod-level default must keep runAsNonRoot for the init container too")
	}
}

// --- item 2: the fallback chain starts with the primary provider ---

func TestFallbackChainEnv(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		model     string
		providers []platformv1alpha1.FallbackProviderEntry
		want      map[string]string
	}{
		{
			name: "primary not listed is prepended with its model", provider: "CLAUDEAI", model: "claude-sonnet-5",
			providers: []platformv1alpha1.FallbackProviderEntry{{Name: "OPENAI", Model: "gpt-6-astra"}},
			want: map[string]string{
				"CHATCLI_FALLBACK_PROVIDERS":      "CLAUDEAI,OPENAI",
				"CHATCLI_FALLBACK_MODEL_CLAUDEAI": "claude-sonnet-5",
				"CHATCLI_FALLBACK_MODEL_OPENAI":   "gpt-6-astra",
			},
		},
		{
			name: "primary already listed keeps the user's order", provider: "OPENAI", model: "gpt-6-astra",
			providers: []platformv1alpha1.FallbackProviderEntry{{Name: "XAI"}, {Name: "OPENAI", Model: "gpt-6-luna"}},
			want: map[string]string{
				"CHATCLI_FALLBACK_PROVIDERS":    "XAI,OPENAI",
				"CHATCLI_FALLBACK_MODEL_OPENAI": "gpt-6-luna",
			},
		},
		{
			name: "no model on the primary sets no model variable", provider: "CLAUDEAI",
			providers: []platformv1alpha1.FallbackProviderEntry{{Name: "OPENAI"}, {Name: "XAI"}},
			want:      map[string]string{"CHATCLI_FALLBACK_PROVIDERS": "CLAUDEAI,OPENAI,XAI"},
		},
		{
			name: "case-insensitive match counts as listed", provider: "openai",
			providers: []platformv1alpha1.FallbackProviderEntry{{Name: "OPENAI"}},
			want:      map[string]string{"CHATCLI_FALLBACK_PROVIDERS": "OPENAI"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := newInstance("fb", "default")
			inst.Spec.Provider, inst.Spec.Model = tc.provider, tc.model
			inst.Spec.Fallback = &platformv1alpha1.FallbackSpec{Enabled: true, Providers: tc.providers}
			got := fallbackChainEnv(inst)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestReconcileConfigMap_FallbackIncludesThePrimary(t *testing.T) {
	inst := newInstance("fb-cm", "default")
	inst.Spec.Fallback = &platformv1alpha1.FallbackSpec{
		Enabled:   true,
		Providers: []platformv1alpha1.FallbackProviderEntry{{Name: "OPENAI", Model: "gpt-6-astra"}},
	}
	r, c := setupFakeReconciler(inst)
	ctx := context.Background()
	if err := r.reconcileConfigMap(ctx, inst); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "fb-cm", Namespace: "default"}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["CHATCLI_FALLBACK_PROVIDERS"] != "CLAUDEAI,OPENAI" {
		t.Errorf("chain = %q, want the primary first", cm.Data["CHATCLI_FALLBACK_PROVIDERS"])
	}
	if cm.Data["CHATCLI_FALLBACK_MODEL_CLAUDEAI"] != "claude-sonnet-4-5" {
		t.Errorf("primary model = %q", cm.Data["CHATCLI_FALLBACK_MODEL_CLAUDEAI"])
	}

	// Disabled: nothing rendered.
	inst.Spec.Fallback.Enabled = false
	if err := r.reconcileConfigMap(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "fb-cm", Namespace: "default"}, &cm); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.Data["CHATCLI_FALLBACK_PROVIDERS"]; ok {
		t.Error("a disabled fallback renders no chain")
	}
}

// --- item 3: every referenced Secret is watched and rolls the pods ---

func fullyReferencedInstance() *platformv1alpha1.Instance {
	inst := newInstance("refs", "default")
	inst.Spec.APIKeys = &platformv1alpha1.SecretRefSpec{Name: "s-apikeys"}
	inst.Spec.Server.Token = &platformv1alpha1.SecretKeyRefSpec{Name: "s-token", Key: "token"}
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true, SecretName: "s-tls", ClientCASecretName: "s-client-ca"}
	inst.Spec.Server.Security = &platformv1alpha1.ServerSecuritySpec{
		JWTSecretRef:                 &platformv1alpha1.SecretKeyRefSpec{Name: "s-jwt", Key: "secret"},
		JWTPublicKeyRef:              &platformv1alpha1.SecretKeyRefSpec{Name: "s-jwt-pub", Key: "key.pem"},
		OperatorTokenRef:             &platformv1alpha1.SecretKeyRefSpec{Name: "s-op-token", Key: "token"},
		OperatorClientCertSecretName: "s-op-cert",
	}
	inst.Spec.Features = &platformv1alpha1.FeaturesSpec{
		CABundleSecretName: "s-ca-bundle",
		EncryptionKeyRef:   &platformv1alpha1.SecretKeyRefSpec{Name: "s-enc", Key: "key"},
	}
	inst.Spec.ExtraEnv = []corev1.EnvVar{{
		Name:      "CUSTOM_SECRET",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s-extra"}, Key: "v"}},
	}}
	return inst
}

func TestSecretToInstance_MapsEveryReferencedSecret(t *testing.T) {
	inst := fullyReferencedInstance()
	other := newInstance("other", "default")
	other.Spec.Server.Token = &platformv1alpha1.SecretKeyRefSpec{Name: "unrelated", Key: "token"}
	r, _ := setupFakeReconciler(inst, other)
	ctx := context.Background()

	for _, name := range []string{
		"s-apikeys", "s-token", "s-tls", "s-client-ca", "s-jwt", "s-jwt-pub",
		"s-op-token", "s-op-cert", "s-ca-bundle", "s-enc", "s-extra",
	} {
		reqs := r.secretToInstance(ctx, secretFixture(name, nil))
		if len(reqs) != 1 || reqs[0].Name != "refs" {
			t.Errorf("secret %s mapped to %v, want [refs]", name, reqs)
		}
	}
	if reqs := r.secretToInstance(ctx, secretFixture("nobody-uses-this", nil)); len(reqs) != 0 {
		t.Errorf("unreferenced secret mapped to %v", reqs)
	}
}

func deploymentAnnotations(t *testing.T, c client.Client, name string) map[string]string {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &d); err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Annotations
}

func updateSecretData(t *testing.T, c client.Client, name, key, value string) {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Data[key] = []byte(value)
	if err := c.Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileDeployment_CredentialRotationRollsThePods(t *testing.T) {
	inst := fullyReferencedInstance()
	objs := []client.Object{
		inst,
		secretFixture("s-token", map[string]string{"token": "t1", "unrelated": "x"}),
		secretFixture("s-jwt", map[string]string{"secret": "jwt-1"}),
		secretFixture("s-client-ca", map[string]string{"ca.crt": "ca-1"}),
		secretFixture("s-op-token", map[string]string{"token": "op-1"}),
	}
	r, c := setupFakeReconciler(objs...)
	ctx := context.Background()

	if err := r.reconcileDeployment(ctx, inst); err != nil {
		t.Fatal(err)
	}
	first := deploymentAnnotations(t, c, "refs")[credentialsHashAnnotation]
	if first == "" {
		t.Fatal("credentials hash annotation missing")
	}

	// Nothing changed: same hash, so no rollout loop.
	if err := r.reconcileDeployment(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if got := deploymentAnnotations(t, c, "refs")[credentialsHashAnnotation]; got != first {
		t.Fatalf("hash moved without a change: %s -> %s", first, got)
	}

	// A key the pod does not read: no rollout.
	updateSecretData(t, c, "s-token", "unrelated", "y")
	// The operator's own credential is not read by the pod: no rollout.
	updateSecretData(t, c, "s-op-token", "token", "op-2")
	if err := r.reconcileDeployment(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if got := deploymentAnnotations(t, c, "refs")[credentialsHashAnnotation]; got != first {
		t.Fatalf("unrelated change rolled the pods: %s -> %s", first, got)
	}

	// Rotating the token rolls the pods.
	updateSecretData(t, c, "s-token", "token", "t2")
	if err := r.reconcileDeployment(ctx, inst); err != nil {
		t.Fatal(err)
	}
	second := deploymentAnnotations(t, c, "refs")[credentialsHashAnnotation]
	if second == first {
		t.Fatal("token rotation did not change the pod template")
	}

	// Rotating the client CA rolls them too.
	updateSecretData(t, c, "s-client-ca", "ca.crt", "ca-2")
	if err := r.reconcileDeployment(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if got := deploymentAnnotations(t, c, "refs")[credentialsHashAnnotation]; got == second {
		t.Fatal("client CA rotation did not change the pod template")
	}
}

func TestReconcileDeployment_NoCredentialRefsNoAnnotation(t *testing.T) {
	inst := newInstance("bare", "default")
	inst.Spec.Server.Token = nil
	r, c := setupFakeReconciler(inst)
	if err := r.reconcileDeployment(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if _, ok := deploymentAnnotations(t, c, "bare")[credentialsHashAnnotation]; ok {
		t.Error("an Instance without credential refs carries no credentials hash")
	}
}

func TestHashSecretUses_MissingHashesAsAbsentAndErrorsAreReturned(t *testing.T) {
	ctx := context.Background()
	uses := []secretKeyUse{{name: "later", keys: []string{"token"}}}
	c := fakeClientWith()
	absent, err := hashSecretUses(ctx, c, "default", uses)
	if err != nil || absent == "" {
		t.Fatalf("missing secret: hash=%q err=%v", absent, err)
	}
	if err := c.Create(ctx, secretFixture("later", map[string]string{"token": "t"})); err != nil {
		t.Fatal(err)
	}
	present, err := hashSecretUses(ctx, c, "default", uses)
	if err != nil || present == absent {
		t.Fatalf("creating the secret must change the hash: %q vs %q (%v)", present, absent, err)
	}

	failing := fake.NewClientBuilder().WithScheme(newScheme()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("apiserver unavailable")
		},
	}).Build()
	if _, err := hashSecretUses(ctx, failing, "default", uses); err == nil {
		t.Error("a transient read error must be returned, never folded into the hash")
	}
	if h, err := hashSecretUses(ctx, failing, "default", nil); err != nil || h != "" {
		t.Errorf("no uses: %q %v", h, err)
	}
}

// --- item 7: periodic probe without status churn ---

func TestUpdateStatus_UnchangedProbeDoesNotRewriteStatus(t *testing.T) {
	inst := newInstance("steady", "default")
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "steady", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	c := fakeClientWith(inst, deploy)
	r := &InstanceReconciler{Client: c, Scheme: newScheme(), Prober: fakeProber{res: ProbeResult{Healthy: true, Version: "1.212.0", Provider: "OPENAI", Model: "gpt-6-astra"}}}
	ctx := context.Background()
	key := types.NamespacedName{Name: "steady", Namespace: "default"}

	get := func() platformv1alpha1.Instance {
		var got platformv1alpha1.Instance
		if err := c.Get(ctx, key, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	cur := get()
	if err := r.updateStatus(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	first := get()
	if first.Status.ServerProbeTime == nil {
		t.Fatal("first probe records its time")
	}

	// Same outcome right after: nothing to write.
	again := first.DeepCopy()
	if err := r.updateStatus(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.ResourceVersion != first.ResourceVersion {
		t.Fatalf("unchanged probe rewrote the status (rv %s -> %s)", first.ResourceVersion, got.ResourceVersion)
	}

	// The stamp is refreshed once it is older than the probe interval.
	stale := get()
	old := metav1.NewTime(time.Now().Add(-serverProbeInterval - time.Minute))
	stale.Status.ServerProbeTime = &old
	if err := c.Status().Update(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	stale = get()
	if err := r.updateStatus(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if got := get(); !got.Status.ServerProbeTime.After(old.Time) {
		t.Error("a stale probe time is refreshed")
	}

	// A new version is recorded at once.
	r.Prober = fakeProber{res: ProbeResult{Healthy: true, Version: "1.213.0"}}
	upgraded := get()
	if err := r.updateStatus(ctx, &upgraded); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.Status.ServerVersion != "1.213.0" {
		t.Errorf("version = %q, want the upgraded one", got.Status.ServerVersion)
	}
}

func TestReconcile_RequeuesAReadyInstanceForTheProbe(t *testing.T) {
	inst := newInstance("periodic", "default")
	ready := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "periodic", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	r, _ := setupFakeReconciler(inst, ready)
	r.Prober = fakeProber{res: ProbeResult{Healthy: true, Version: "1.212.0"}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "periodic", Namespace: "default"}}

	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != serverProbeInterval {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, serverProbeInterval)
	}

	// Without a prober there is nothing to refresh.
	r.Prober = nil
	res, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter without prober = %v", res.RequeueAfter)
	}

	// Not ready: the Deployment's own status events drive the next reconcile.
	notReady := newInstance("waiting", "default")
	r2, _ := setupFakeReconciler(notReady)
	r2.Prober = fakeProber{res: ProbeResult{Healthy: true}}
	res, err = r2.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waiting", Namespace: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter while not ready = %v", res.RequeueAfter)
	}
}

func TestProbeStampStale(t *testing.T) {
	now := time.Now()
	if !probeStampStale(nil, now) {
		t.Error("no stamp is stale")
	}
	fresh := metav1.NewTime(now.Add(-time.Minute))
	if probeStampStale(&fresh, now) {
		t.Error("a one-minute-old stamp is fresh")
	}
	old := metav1.NewTime(now.Add(-serverProbeInterval))
	if !probeStampStale(&old, now) {
		t.Error("a stamp one interval old is stale")
	}
}

// --- item 10: TLS enabled without a certificate Secret ---

func TestReconcile_TLSWithoutSecretNameIsNotProvisioned(t *testing.T) {
	inst := newInstance("tls-missing", "default")
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true}
	r, c := setupFakeReconciler(inst)
	ctx := context.Background()
	key := types.NamespacedName{Name: "tls-missing", Namespace: "default"}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile should stop cleanly: %v", err)
	}
	var got platformv1alpha1.Instance
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, TLSConditionType)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "SecretNameMissing" {
		t.Fatalf("TLS condition = %+v, want False/SecretNameMissing", cond)
	}
	if got.Status.Ready {
		t.Error("a blocked Instance is not ready")
	}
	var d appsv1.Deployment
	if err := c.Get(ctx, key, &d); err == nil {
		t.Fatal("no Deployment for a server that cannot load its certificate")
	}

	// Fixed with a Secret name: provisioned, condition True.
	got.Spec.Server.TLS.SecretName = "tls-pair"
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, TLSConditionType); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("TLS condition after fix = %+v", cond)
	}
	if err := c.Get(ctx, key, &d); err != nil {
		t.Fatalf("Deployment after fix: %v", err)
	}

	// TLS turned off: the condition goes away.
	got.Spec.Server.TLS.Enabled = false
	if err := c.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(got.Status.Conditions, TLSConditionType) != nil {
		t.Error("TLS condition lingers after TLS was disabled")
	}
}

func TestReconcile_TLSAndAuthPrechecksAreBothReported(t *testing.T) {
	inst := newInstance("both-wrong", "default")
	inst.Spec.Server.Token = nil
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true}
	r, c := setupFakeReconciler(inst)
	ctx := context.Background()
	key := types.NamespacedName{Name: "both-wrong", Namespace: "default"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.Instance
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{TLSConditionType, AuthConditionType} {
		if cond := meta.FindStatusCondition(got.Status.Conditions, typ); cond == nil || cond.Status != metav1.ConditionFalse {
			t.Errorf("%s = %+v, want False", typ, cond)
		}
	}
}
