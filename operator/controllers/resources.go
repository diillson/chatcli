package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func labels(instance *platformv1alpha1.Instance) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "chatcli",
		"app.kubernetes.io/instance":   instance.Name,
		"app.kubernetes.io/managed-by": "chatcli-operator",
	}
}

// reconcileDeployment creates or updates the ChatCLI Deployment.
func (r *InstanceReconciler) reconcileDeployment(ctx context.Context, instance *platformv1alpha1.Instance) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(instance, deploy, r.Scheme); err != nil {
			return err
		}

		replicas := int32(1)
		if instance.Spec.Replicas != nil {
			replicas = *instance.Spec.Replicas
		}

		deploy.Labels = labels(instance)
		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels(instance),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels(instance),
				},
				Spec: r.buildPodSpec(instance),
			},
		}
		return nil
	})
	return err
}

func (r *InstanceReconciler) buildPodSpec(instance *platformv1alpha1.Instance) corev1.PodSpec {
	// Build container args
	args := r.buildContainerArgs(instance)

	// Image
	repo := "ghcr.io/diillson/chatcli"
	tag := "latest"
	pullPolicy := corev1.PullIfNotPresent
	if instance.Spec.Image.Repository != "" {
		repo = instance.Spec.Image.Repository
	}
	if instance.Spec.Image.Tag != "" {
		tag = instance.Spec.Image.Tag
	}
	if instance.Spec.Image.PullPolicy != "" {
		pullPolicy = instance.Spec.Image.PullPolicy
	}

	port := int32(50051)
	if instance.Spec.Server.Port > 0 {
		port = instance.Spec.Server.Port
	}

	metricsPort := int32(9090)
	if instance.Spec.Server.MetricsPort > 0 {
		metricsPort = instance.Spec.Server.MetricsPort
	}

	container := corev1.Container{
		Name:            "chatcli",
		Image:           fmt.Sprintf("%s:%s", repo, tag),
		ImagePullPolicy: pullPolicy,
		Command:         []string{"chatcli", "serve"},
		Args:            args,
		Ports: []corev1.ContainerPort{
			{
				Name:          "grpc",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: metricsPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: instance.Spec.Resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}

	// EnvFrom: ConfigMap
	container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
		ConfigMapRef: &corev1.ConfigMapEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: instance.Name},
		},
	})

	// EnvFrom: API Keys Secret
	if instance.Spec.APIKeys != nil {
		container.EnvFrom = append(container.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: instance.Spec.APIKeys.Name},
			},
		})
	}

	// Volume mounts
	var volumeMounts []corev1.VolumeMount
	var volumes []corev1.Volume

	// /tmp for read-only rootfs
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "tmp",
		MountPath: "/tmp",
	})
	volumes = append(volumes, corev1.Volume{
		Name: "tmp",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: resourceQuantity("100Mi"),
			},
		},
	})

	// Sessions PVC
	if instance.Spec.Persistence != nil && instance.Spec.Persistence.Enabled {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "sessions",
			MountPath: "/home/chatcli/.chatcli/sessions",
		})
		volumes = append(volumes, corev1.Volume{
			Name: "sessions",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: instance.Name + "-sessions",
				},
			},
		})
	}

	// TLS Secret
	if instance.Spec.Server.TLS != nil && instance.Spec.Server.TLS.Enabled && instance.Spec.Server.TLS.SecretName != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "tls",
			MountPath: "/etc/chatcli/tls",
			ReadOnly:  true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: instance.Spec.Server.TLS.SecretName,
				},
			},
		})
	}

	// Watch config volume (multi-target mode)
	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled && len(instance.Spec.Watcher.Targets) > 0 {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "watch-config",
			MountPath: "/etc/chatcli/watch",
			ReadOnly:  true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: "watch-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.Name + "-watch-config",
					},
				},
			},
		})
	}

	container.VolumeMounts = volumeMounts

	podSpec := corev1.PodSpec{
		ServiceAccountName: instance.Name,
		Containers:         []corev1.Container{container},
		Volumes:            volumes,
	}

	if instance.Spec.SecurityContext != nil {
		podSpec.SecurityContext = instance.Spec.SecurityContext
	} else {
		// Default security context
		podSpec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: boolPtr(true),
			RunAsUser:    int64Ptr(1000),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
	}

	return podSpec
}

func (r *InstanceReconciler) buildContainerArgs(instance *platformv1alpha1.Instance) []string {
	var args []string

	port := int32(50051)
	if instance.Spec.Server.Port > 0 {
		port = instance.Spec.Server.Port
	}
	args = append(args, "--port", strconv.Itoa(int(port)))

	// Metrics port (default 9090, enables Prometheus endpoint)
	metricsPort := int32(9090)
	if instance.Spec.Server.MetricsPort > 0 {
		metricsPort = instance.Spec.Server.MetricsPort
	}
	args = append(args, "--metrics-port", strconv.Itoa(int(metricsPort)))

	if instance.Spec.Provider != "" {
		args = append(args, "--provider", instance.Spec.Provider)
	}
	if instance.Spec.Model != "" {
		args = append(args, "--model", instance.Spec.Model)
	}

	// TLS args
	if instance.Spec.Server.TLS != nil && instance.Spec.Server.TLS.Enabled {
		args = append(args, "--tls-cert", "/etc/chatcli/tls/tls.crt")
		args = append(args, "--tls-key", "/etc/chatcli/tls/tls.key")
	}

	// Watcher args
	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled {
		if len(instance.Spec.Watcher.Targets) > 0 {
			// Multi-target mode: use config file
			args = append(args, "--watch-config", "/etc/chatcli/watch/watch-config.yaml")
		} else if instance.Spec.Watcher.Deployment != "" {
			// Legacy single-target mode
			args = append(args, "--watch-deployment", instance.Spec.Watcher.Deployment)
			if instance.Spec.Watcher.Namespace != "" {
				args = append(args, "--watch-namespace", instance.Spec.Watcher.Namespace)
			}
			if instance.Spec.Watcher.Interval != "" {
				args = append(args, "--watch-interval", instance.Spec.Watcher.Interval)
			}
			if instance.Spec.Watcher.Window != "" {
				args = append(args, "--watch-window", instance.Spec.Watcher.Window)
			}
			if instance.Spec.Watcher.MaxLogLines > 0 {
				args = append(args, "--watch-max-log-lines", strconv.Itoa(int(instance.Spec.Watcher.MaxLogLines)))
			}
		}
	}

	return args
}

// reconcileService creates or updates the ClusterIP Service for gRPC.
func (r *InstanceReconciler) reconcileService(ctx context.Context, instance *platformv1alpha1.Instance) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(instance, svc, r.Scheme); err != nil {
			return err
		}

		port := int32(50051)
		if instance.Spec.Server.Port > 0 {
			port = instance.Spec.Server.Port
		}

		metricsPort := int32(9090)
		if instance.Spec.Server.MetricsPort > 0 {
			metricsPort = instance.Spec.Server.MetricsPort
		}

		svc.Labels = labels(instance)
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels(instance),
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       port,
					TargetPort: intstr.FromString("grpc"),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "metrics",
					Port:       metricsPort,
					TargetPort: intstr.FromString("metrics"),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return nil
	})
	return err
}

// reconcileConfigMap creates or updates the ConfigMap with LLM/watcher env vars.
func (r *InstanceReconciler) reconcileConfigMap(ctx context.Context, instance *platformv1alpha1.Instance) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(instance, cm, r.Scheme); err != nil {
			return err
		}

		cm.Labels = labels(instance)
		cm.Data = map[string]string{
			"LLM_PROVIDER": instance.Spec.Provider,
		}
		if instance.Spec.Model != "" {
			cm.Data["LLM_MODEL"] = instance.Spec.Model
		}
		if instance.Spec.Server.Port > 0 {
			cm.Data["CHATCLI_SERVER_PORT"] = strconv.Itoa(int(instance.Spec.Server.Port))
		}
		if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled {
			cm.Data["CHATCLI_WATCH_DEPLOYMENT"] = instance.Spec.Watcher.Deployment
			cm.Data["CHATCLI_WATCH_NAMESPACE"] = instance.Spec.Watcher.Namespace
			if instance.Spec.Watcher.Interval != "" {
				cm.Data["CHATCLI_WATCH_INTERVAL"] = instance.Spec.Watcher.Interval
			}
			if instance.Spec.Watcher.Window != "" {
				cm.Data["CHATCLI_WATCH_WINDOW"] = instance.Spec.Watcher.Window
			}
			if instance.Spec.Watcher.MaxLogLines > 0 {
				cm.Data["CHATCLI_WATCH_MAX_LOG_LINES"] = strconv.Itoa(int(instance.Spec.Watcher.MaxLogLines))
			}
		}
		return nil
	})
	return err
}

// reconcileWatchConfigMap creates the ConfigMap with multi-target watch config YAML.
func (r *InstanceReconciler) reconcileWatchConfigMap(ctx context.Context, instance *platformv1alpha1.Instance) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-watch-config",
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(instance, cm, r.Scheme); err != nil {
			return err
		}

		cm.Labels = labels(instance)
		cm.Data = map[string]string{
			"watch-config.yaml": buildWatchConfigYAML(instance.Spec.Watcher),
		}
		return nil
	})
	return err
}

// buildWatchConfigYAML generates the multi-target watch config YAML from the CRD spec.
func buildWatchConfigYAML(watcher *platformv1alpha1.WatcherSpec) string {
	var b strings.Builder

	if watcher.Interval != "" {
		b.WriteString(fmt.Sprintf("interval: %q\n", watcher.Interval))
	}
	if watcher.Window != "" {
		b.WriteString(fmt.Sprintf("window: %q\n", watcher.Window))
	}
	if watcher.MaxLogLines > 0 {
		b.WriteString(fmt.Sprintf("maxLogLines: %d\n", watcher.MaxLogLines))
	}
	if watcher.MaxContextChars > 0 {
		b.WriteString(fmt.Sprintf("maxContextChars: %d\n", watcher.MaxContextChars))
	}

	b.WriteString("targets:\n")
	for _, t := range watcher.Targets {
		b.WriteString(fmt.Sprintf("  - deployment: %q\n", t.Deployment))
		ns := t.Namespace
		if ns == "" {
			ns = "default"
		}
		b.WriteString(fmt.Sprintf("    namespace: %q\n", ns))
		if t.MetricsPort > 0 {
			b.WriteString(fmt.Sprintf("    metricsPort: %d\n", t.MetricsPort))
		}
		if t.MetricsPath != "" {
			b.WriteString(fmt.Sprintf("    metricsPath: %q\n", t.MetricsPath))
		}
		if len(t.MetricsFilter) > 0 {
			b.WriteString("    metricsFilter:\n")
			for _, f := range t.MetricsFilter {
				b.WriteString(fmt.Sprintf("      - %q\n", f))
			}
		}
	}

	return b.String()
}

// reconcileServiceAccount creates or updates the ServiceAccount.
func (r *InstanceReconciler) reconcileServiceAccount(ctx context.Context, instance *platformv1alpha1.Instance) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		if err := controllerutil.SetControllerReference(instance, sa, r.Scheme); err != nil {
			return err
		}
		sa.Labels = labels(instance)
		return nil
	})
	return err
}

// watcherPolicyRules returns the RBAC rules needed by the K8s watcher.
func watcherPolicyRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/log", "events", "services", "endpoints"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"apps"},
			Resources: []string{"deployments", "replicasets", "statefulsets", "daemonsets"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"autoscaling"},
			Resources: []string{"horizontalpodautoscalers"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"networking.k8s.io"},
			Resources: []string{"ingresses"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"metrics.k8s.io"},
			Resources: []string{"pods"},
			Verbs:     []string{"get", "list"},
		},
	}
}

// needsClusterRBAC returns true if multi-target watches span multiple namespaces.
func needsClusterRBAC(instance *platformv1alpha1.Instance) bool {
	if instance.Spec.Watcher == nil || len(instance.Spec.Watcher.Targets) <= 1 {
		return false
	}
	namespaces := make(map[string]struct{})
	for _, t := range instance.Spec.Watcher.Targets {
		ns := t.Namespace
		if ns == "" {
			ns = "default"
		}
		namespaces[ns] = struct{}{}
	}
	return len(namespaces) > 1
}

// reconcileRBAC creates Role + RoleBinding (or ClusterRole + ClusterRoleBinding for multi-namespace).
func (r *InstanceReconciler) reconcileRBAC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	if needsClusterRBAC(instance) {
		return r.reconcileClusterRBAC(ctx, instance)
	}
	return r.reconcileNamespacedRBAC(ctx, instance)
}

// reconcileNamespacedRBAC creates Role + RoleBinding for single-namespace watcher access.
func (r *InstanceReconciler) reconcileNamespacedRBAC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-watcher",
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(instance, role, r.Scheme); err != nil {
			return err
		}
		role.Labels = labels(instance)
		role.Rules = watcherPolicyRules()
		return nil
	})
	if err != nil {
		return err
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-watcher",
			Namespace: instance.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		if err := controllerutil.SetControllerReference(instance, rb, r.Scheme); err != nil {
			return err
		}
		rb.Labels = labels(instance)
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		}
		rb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      instance.Name,
				Namespace: instance.Namespace,
			},
		}
		return nil
	})
	return err
}

// reconcileClusterRBAC creates ClusterRole + ClusterRoleBinding for multi-namespace watcher access.
func (r *InstanceReconciler) reconcileClusterRBAC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: instance.Namespace + "-" + instance.Name + "-watcher",
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, clusterRole, func() error {
		clusterRole.Labels = labels(instance)
		clusterRole.Rules = watcherPolicyRules()
		return nil
	})
	if err != nil {
		return err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: instance.Namespace + "-" + instance.Name + "-watcher",
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.Labels = labels(instance)
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRole.Name,
		}
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      instance.Name,
				Namespace: instance.Namespace,
			},
		}
		return nil
	})
	return err
}

// reconcilePVC creates the PersistentVolumeClaim for session persistence.
func (r *InstanceReconciler) reconcilePVC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	pvcName := instance.Name + "-sessions"

	// Check if PVC already exists (PVCs are immutable after creation)
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: instance.Namespace}, existing)
	if err == nil {
		// PVC already exists, nothing to do
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create new PVC
	size := "1Gi"
	if instance.Spec.Persistence.Size != "" {
		size = instance.Spec.Persistence.Size
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: instance.Namespace,
			Labels:    labels(instance),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}

	if instance.Spec.Persistence.StorageClassName != nil {
		pvc.Spec.StorageClassName = instance.Spec.Persistence.StorageClassName
	}

	if err := controllerutil.SetControllerReference(instance, pvc, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, pvc)
}

func boolPtr(b bool) *bool {
	return &b
}

func int64Ptr(i int64) *int64 {
	return &i
}

func resourceQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}
