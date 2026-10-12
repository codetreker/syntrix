package provisioning

import (
	"fmt"
	"maps"
	"slices"

	"github.com/codetreker/syntrix/console-server/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ptr[T any](v T) *T { return &v }

func deploymentFor(w workload.Workload) (*appsv1.Deployment, error) {
	t := w.Snapshot
	quantities := make([]resource.Quantity, 4)
	for i, value := range []string{t.CPURequest, t.MemoryRequest, t.CPULimit, t.MemoryLimit} {
		q, err := resource.ParseQuantity(value)
		if err != nil || q.Sign() <= 0 {
			return nil, fmt.Errorf("invalid resource quantity")
		}
		q.RoundUp(resource.Milli)
		quantities[i] = q
	}
	if quantities[0].Cmp(quantities[2]) > 0 || quantities[1].Cmp(quantities[3]) > 0 {
		return nil, fmt.Errorf("resource request exceeds limit")
	}
	if t.Image == "" || t.Port < 1 || t.Port > 65535 || t.ProbePath == "" || t.ProbePath[0] != '/' {
		return nil, fmt.Errorf("invalid container template")
	}
	names := slices.Sorted(maps.Keys(t.Env))
	env := make([]corev1.EnvVar, 0, len(names))
	for _, name := range names {
		env = append(env, corev1.EnvVar{Name: name, Value: t.Env[name]})
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: w.DeploymentName, Namespace: w.Namespace, Labels: labelsFor(w), Annotations: annotationsFor(w)},
		Spec: appsv1.DeploymentSpec{
			Replicas:                ptr(int32(1)),
			Selector:                &metav1.LabelSelector{MatchLabels: labelsFor(w)},
			Strategy:                appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			RevisionHistoryLimit:    ptr(int32(0)),
			ProgressDeadlineSeconds: ptr(int32(600)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labelsFor(w), Annotations: annotationsFor(w)},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  ptr(false),
					ServiceAccountName:            "default",
					RestartPolicy:                 corev1.RestartPolicyAlways,
					DNSPolicy:                     corev1.DNSClusterFirst,
					SchedulerName:                 corev1.DefaultSchedulerName,
					TerminationGracePeriodSeconds: ptr(int64(30)),
					EnableServiceLinks:            ptr(false),
					SecurityContext:               &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{
						Name: "service", Image: t.Image, ImagePullPolicy: corev1.PullIfNotPresent,
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: t.Port, Protocol: corev1.ProtocolTCP}},
						Env:   env,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: quantities[0], corev1.ResourceMemory: quantities[1]},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: quantities[2], corev1.ResourceMemory: quantities[3]},
						},
						SecurityContext:          &corev1.SecurityContext{Privileged: ptr(false), AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
						ReadinessProbe:           &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: t.ProbePath, Port: intstr.FromString("http"), Scheme: corev1.URISchemeHTTP}}, TimeoutSeconds: 1, PeriodSeconds: 5, SuccessThreshold: 1, FailureThreshold: 3},
						TerminationMessagePath:   corev1.TerminationMessagePathDefault,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
					}},
				},
			},
		},
	}, nil
}

func serviceFor(w workload.Workload, deploymentUID types.UID) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: w.ServiceName, Namespace: w.Namespace, Labels: labelsFor(w), Annotations: annotationsFor(w), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: w.DeploymentName, UID: deploymentUID, Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeClusterIP,
			Selector:              labelsFor(w),
			Ports:                 []corev1.ServicePort{{Name: "http", Port: w.Snapshot.Port, TargetPort: intstr.FromString("http"), Protocol: corev1.ProtocolTCP}},
			SessionAffinity:       corev1.ServiceAffinityNone,
			InternalTrafficPolicy: ptr(corev1.ServiceInternalTrafficPolicyCluster),
		},
	}
}

func verifyMetadata(actual metav1.Object, desired metav1.Object, expectedUID string) error {
	if actual.GetUID() == "" || (expectedUID != "" && string(actual.GetUID()) != expectedUID) ||
		actual.GetNamespace() != desired.GetNamespace() || actual.GetName() != desired.GetName() {
		return failure("kubernetes_resource_identity_conflict", nil)
	}
	for key, value := range desired.GetLabels() {
		if actual.GetLabels()[key] != value {
			return failure("kubernetes_resource_ownership_conflict", nil)
		}
	}
	for key, value := range desired.GetAnnotations() {
		if actual.GetAnnotations()[key] != value {
			return failure("kubernetes_resource_ownership_conflict", nil)
		}
	}
	if !equality.Semantic.DeepEqual(actual.GetOwnerReferences(), desired.GetOwnerReferences()) {
		return failure("kubernetes_resource_ownership_conflict", nil)
	}
	return nil
}

func verifyDeployment(actual, desired *appsv1.Deployment, expectedUID string) error {
	if err := verifyMetadata(actual, desired, expectedUID); err != nil {
		return err
	}
	normalized := actual.Spec.DeepCopy()
	// The API conversion writes serviceAccount as an alias of serviceAccountName.
	// https://github.com/kubernetes/kubernetes/blob/v1.35.5/pkg/apis/core/v1/conversion.go#L298-L305
	if normalized.Template.Spec.DeprecatedServiceAccount == normalized.Template.Spec.ServiceAccountName {
		normalized.Template.Spec.DeprecatedServiceAccount = ""
	}
	if !equality.Semantic.DeepEqual(*normalized, desired.Spec) {
		return failure("kubernetes_deployment_spec_conflict", nil)
	}
	return nil
}

func verifyService(actual, desired *corev1.Service, expectedUID string) error {
	if err := verifyMetadata(actual, desired, expectedUID); err != nil {
		return err
	}
	normalized := actual.Spec.DeepCopy()
	// Cluster allocation belongs to the API server. Everything that determines
	// routing, exposure, or ownership remains part of the immutable template.
	normalized.ClusterIP = ""
	normalized.ClusterIPs = nil
	normalized.IPFamilies = nil
	normalized.IPFamilyPolicy = nil
	if !equality.Semantic.DeepEqual(*normalized, desired.Spec) {
		return failure("kubernetes_service_spec_conflict", nil)
	}
	return nil
}
