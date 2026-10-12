package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/provisioning/config"
	"github.com/codetreker/syntrix/console-server/internal/workload"
	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedapps "k8s.io/client-go/kubernetes/typed/apps/v1"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	typeddiscovery "k8s.io/client-go/kubernetes/typed/discovery/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	installationLabel   = "console.syntrix.io/installation"
	workloadLabel       = "console.syntrix.io/workload"
	developerAnnotation = "console.syntrix.io/developer"
	digestAnnotation    = "console.syntrix.io/template-digest"
)

var workloadIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Executor struct {
	cfg       config.Config
	apps      typedapps.AppsV1Interface
	core      typedcore.CoreV1Interface
	discovery typeddiscovery.DiscoveryV1Interface
}

// Failure preserves the underlying error for classification without including
// Kubernetes response bodies, which can contain configured environment values.
type Failure struct {
	Reason string
	cause  error
}

func (e *Failure) Error() string         { return e.Reason }
func (e *Failure) Unwrap() error         { return e.cause }
func (e *Failure) ErrorCategory() string { return e.Reason }

func failure(reason string, cause error) error {
	return &Failure{Reason: reason, cause: cause}
}

func New(cfg config.Config) (*Executor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var rc *rest.Config
	var err error
	if cfg.Mode == "in_cluster" {
		rc, err = rest.InClusterConfig()
	} else {
		var raw *clientcmdapi.Config
		raw, err = clientcmd.LoadFromFile(cfg.Kubeconfig)
		if err == nil {
			err = clientcmd.ResolveLocalPaths(raw)
		}
		if err == nil {
			selected := raw.Contexts[cfg.Context]
			if selected == nil || raw.Clusters[selected.Cluster] == nil || raw.Clusters[selected.Cluster].Server == "" {
				err = failure("kubernetes_explicit_context_invalid", nil)
			} else {
				rc, err = clientcmd.NewNonInteractiveClientConfig(*raw, cfg.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			}
		}
	}
	if err != nil {
		return nil, failure("kubernetes_client_configuration_failed", err)
	}
	rc.Timeout = cfg.RequestTimeout
	rc.UserAgent = "syntrix-console-server"
	apps, err := typedapps.NewForConfig(rc)
	if err != nil {
		return nil, failure("kubernetes_client_initialization_failed", err)
	}
	core, err := typedcore.NewForConfig(rc)
	if err != nil {
		return nil, failure("kubernetes_client_initialization_failed", err)
	}
	discovery, err := typeddiscovery.NewForConfig(rc)
	if err != nil {
		return nil, failure("kubernetes_client_initialization_failed", err)
	}
	return &Executor{cfg: cfg, apps: apps, core: core, discovery: discovery}, nil
}

func (e *Executor) validate(w workload.Workload) error {
	if w.Namespace != e.cfg.Namespace || w.InstallationID != e.cfg.InstallationID {
		return failure("kubernetes_target_changed", nil)
	}
	if !workloadIDPattern.MatchString(w.ID) || w.DeveloperID == "" ||
		w.DeploymentName != "w-"+w.ID || w.ServiceName != "w-"+w.ID ||
		w.TemplateDigest == "" || w.TemplateID != w.Snapshot.ID {
		return failure("kubernetes_workload_identity_invalid", nil)
	}
	snapshot, err := json.Marshal(w.Snapshot)
	if err != nil {
		return failure("kubernetes_template_invalid", err)
	}
	digest := sha256.Sum256(snapshot)
	if hex.EncodeToString(digest[:]) != w.TemplateDigest {
		return failure("kubernetes_template_digest_invalid", nil)
	}
	templates := workloadconfig.DefaultConfig()
	templates.Templates = []workload.Template{w.Snapshot}
	if err := templates.Validate(); err != nil {
		return failure("kubernetes_template_invalid", err)
	}
	return nil
}

func observation(w workload.Workload) workload.Observation {
	return workload.Observation{DeploymentUID: w.DeploymentUID, ServiceUID: w.ServiceUID, ObservedAt: time.Now().UTC()}
}

func failed(o workload.Observation, err error) (workload.Observation, error) {
	o.Reason = err.Error()
	return o, err
}

func apiFailure(operation string, err error) error {
	code := "failed"
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		code = "access_denied"
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		code = "conflict"
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "canceled"
	}
	return failure("kubernetes_"+operation+"_"+code, err)
}

func (e *Executor) Ensure(ctx context.Context, w workload.Workload) (workload.Observation, error) {
	o := observation(w)
	if err := e.validate(w); err != nil {
		return failed(o, err)
	}
	if w.DesiredState != workload.DesiredRunning {
		return failed(o, failure("kubernetes_workload_not_running", nil))
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.RequestTimeout)
	defer cancel()
	desiredDeployment, err := deploymentFor(w)
	if err != nil {
		return failed(o, failure("kubernetes_template_invalid", err))
	}
	deployment, err := e.ensureDeployment(ctx, w, desiredDeployment)
	if err != nil {
		return failed(o, err)
	}
	o.DeploymentUID = string(deployment.UID)
	service, err := e.ensureService(ctx, w, serviceFor(w, deployment.UID))
	if err != nil {
		return failed(o, err)
	}
	o.ServiceUID = string(service.UID)
	if deployment.DeletionTimestamp != nil || service.DeletionTimestamp != nil {
		return failed(o, failure("kubernetes_resource_deleting", nil))
	}
	if !deploymentReady(deployment) {
		o.Reason = "waiting_for_deployment"
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
				return failed(o, failure("deployment_progress_deadline_exceeded", nil))
			}
		}
		return o, nil
	}
	ready, err := e.endpointsReady(ctx, w, deployment, service)
	if err != nil {
		return failed(o, err)
	}
	o.Ready = ready
	if !ready {
		o.Reason = "waiting_for_service_endpoints"
	}
	return o, nil
}

func (e *Executor) ensureDeployment(ctx context.Context, w workload.Workload, desired *appsv1.Deployment) (*appsv1.Deployment, error) {
	client := e.apps.Deployments(w.Namespace)
	actual, err := client.Get(ctx, w.DeploymentName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if w.DeploymentUID != "" {
			return nil, failure("kubernetes_deployment_missing", nil)
		}
		actual, err = client.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			actual, err = client.Get(ctx, w.DeploymentName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return nil, apiFailure("deployment_ensure", err)
	}
	if err := verifyDeployment(actual, desired, w.DeploymentUID); err != nil {
		return nil, err
	}
	return actual, nil
}

func (e *Executor) ensureService(ctx context.Context, w workload.Workload, desired *corev1.Service) (*corev1.Service, error) {
	client := e.core.Services(w.Namespace)
	actual, err := client.Get(ctx, w.ServiceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if w.ServiceUID != "" {
			return nil, failure("kubernetes_service_missing", nil)
		}
		actual, err = client.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			actual, err = client.Get(ctx, w.ServiceName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return nil, apiFailure("service_ensure", err)
	}
	if err := verifyService(actual, desired, w.ServiceUID); err != nil {
		return nil, err
	}
	return actual, nil
}

func (e *Executor) Delete(ctx context.Context, w workload.Workload) (workload.Observation, error) {
	o := observation(w)
	if err := e.validate(w); err != nil {
		return failed(o, err)
	}
	if w.DesiredState != workload.DesiredDeleted {
		return failed(o, failure("kubernetes_workload_not_deleted", nil))
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.RequestTimeout)
	defer cancel()
	desired, err := deploymentFor(w)
	if err != nil {
		return failed(o, failure("kubernetes_template_invalid", err))
	}
	deployment, err := e.apps.Deployments(w.Namespace).Get(ctx, w.DeploymentName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		deployment = nil
	} else if err != nil {
		return failed(o, apiFailure("deployment_read", err))
	} else {
		if err := verifyDeployment(deployment, desired, w.DeploymentUID); err != nil {
			return failed(o, err)
		}
		o.DeploymentUID = string(deployment.UID)
	}
	service, err := e.core.Services(w.Namespace).Get(ctx, w.ServiceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		service = nil
	} else if err != nil {
		return failed(o, apiFailure("service_read", err))
	} else {
		if o.DeploymentUID == "" {
			owner := metav1.GetControllerOf(service)
			if owner == nil || owner.Kind != "Deployment" || owner.APIVersion != "apps/v1" || owner.Name != w.DeploymentName || owner.UID == "" {
				return failed(o, failure("kubernetes_service_ownership_conflict", nil))
			}
			o.DeploymentUID = string(owner.UID)
		}
		if err := verifyService(service, serviceFor(w, types.UID(o.DeploymentUID)), w.ServiceUID); err != nil {
			return failed(o, err)
		}
		o.ServiceUID = string(service.UID)
		if service.DeletionTimestamp == nil {
			uid := service.UID
			err := e.core.Services(w.Namespace).Delete(ctx, service.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
			if err != nil && !apierrors.IsNotFound(err) {
				return failed(o, apiFailure("service_delete", err))
			}
		}
	}
	if deployment != nil && deployment.DeletionTimestamp == nil {
		uid := deployment.UID
		foreground := metav1.DeletePropagationForeground
		err := e.apps.Deployments(w.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &foreground})
		if err != nil && !apierrors.IsNotFound(err) {
			return failed(o, apiFailure("deployment_delete", err))
		}
	}
	if deployment != nil || service != nil {
		o.Reason = "waiting_for_resource_deletion"
		return o, nil
	}
	remaining, err := e.childrenRemain(ctx, w, o)
	if err != nil {
		return failed(o, err)
	}
	o.Deleted = !remaining
	if remaining {
		o.Reason = "waiting_for_dependent_deletion"
	}
	return o, nil
}

func labelsFor(w workload.Workload) map[string]string {
	return map[string]string{installationLabel: w.InstallationID, workloadLabel: w.ID}
}

func annotationsFor(w workload.Workload) map[string]string {
	return map[string]string{developerAnnotation: w.DeveloperID, digestAnnotation: w.TemplateDigest, installationLabel: w.InstallationID, workloadLabel: w.ID}
}

func owns(owner metav1.Object, kind, name string, uid types.UID) bool {
	ref := metav1.GetControllerOf(owner)
	apiVersion := "apps/v1"
	if kind == "Service" {
		apiVersion = "v1"
	}
	return ref != nil && ref.APIVersion == apiVersion && ref.Kind == kind && ref.Name == name && ref.UID == uid
}

func deploymentReady(d *appsv1.Deployment) bool {
	return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == 1 &&
		d.Status.Replicas == 1 && d.Status.ReadyReplicas >= 1 && d.Status.AvailableReplicas >= 1 && d.Status.UnavailableReplicas == 0
}

func (e *Executor) endpointsReady(ctx context.Context, w workload.Workload, deployment *appsv1.Deployment, service *corev1.Service) (bool, error) {
	slices, err := e.discovery.EndpointSlices(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + service.Name})
	if err != nil {
		return false, apiFailure("endpoints_read", err)
	}
	for _, slice := range slices.Items {
		if slice.DeletionTimestamp != nil || !owns(&slice, "Service", service.Name, service.UID) {
			continue
		}
		portMatches := false
		for _, port := range slice.Ports {
			if port.Name != nil && *port.Name == "http" && port.Port != nil && *port.Port == w.Snapshot.Port && port.Protocol != nil && *port.Protocol == corev1.ProtocolTCP {
				portMatches = true
			}
		}
		if !portMatches {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			ref := endpoint.TargetRef
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready ||
				(endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating) ||
				len(endpoint.Addresses) == 0 || ref == nil || ref.Kind != "Pod" || ref.Namespace != w.Namespace || ref.UID == "" {
				continue
			}
			pod, err := e.core.Pods(w.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return false, apiFailure("pod_read", err)
			}
			if pod.UID != ref.UID || pod.DeletionTimestamp != nil || !metadataMatches(pod, w) {
				continue
			}
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.Kind != "ReplicaSet" || owner.APIVersion != "apps/v1" {
				continue
			}
			rs, err := e.apps.ReplicaSets(w.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return false, apiFailure("replicaset_read", err)
			}
			if rs.UID != owner.UID || rs.DeletionTimestamp != nil || !owns(rs, "Deployment", deployment.Name, deployment.UID) {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func metadataMatches(obj metav1.Object, w workload.Workload) bool {
	return obj.GetNamespace() == w.Namespace && labelsMatch(obj, w) && annotationsMatch(obj, w)
}

func labelsMatch(obj metav1.Object, w workload.Workload) bool {
	for key, value := range labelsFor(w) {
		if obj.GetLabels()[key] != value {
			return false
		}
	}
	return true
}

func annotationsMatch(obj metav1.Object, w workload.Workload) bool {
	for key, value := range annotationsFor(w) {
		if obj.GetAnnotations()[key] != value {
			return false
		}
	}
	return true
}

func (e *Executor) childrenRemain(ctx context.Context, w workload.Workload, o workload.Observation) (bool, error) {
	rsList, err := e.apps.ReplicaSets(w.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, apiFailure("replicasets_read", err)
	}
	for _, rs := range rsList.Items {
		if o.DeploymentUID != "" && owns(&rs, "Deployment", w.DeploymentName, types.UID(o.DeploymentUID)) {
			return true, nil
		}
		if o.DeploymentUID == "" {
			ref := metav1.GetControllerOf(&rs)
			if annotationsMatch(&rs, w) || labelsMatch(&rs, w) ||
				(ref != nil && ref.APIVersion == "apps/v1" && ref.Kind == "Deployment" && ref.Name == w.DeploymentName) {
				return true, nil
			}
		}
	}
	pods, err := e.core.Pods(w.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, apiFailure("pods_read", err)
	}
	for _, pod := range pods.Items {
		// A matching orphan remains observable after its ReplicaSet disappears.
		// We wait for garbage collection rather than deleting by labels.
		if annotationsMatch(&pod, w) || labelsMatch(&pod, w) {
			return true, nil
		}
		ref := metav1.GetControllerOf(&pod)
		if ref != nil && ref.APIVersion == "apps/v1" && ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.Name, w.DeploymentName+"-") {
			return true, nil
		}
	}
	if o.ServiceUID != "" {
		slices, err := e.discovery.EndpointSlices(w.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, apiFailure("endpoints_read", err)
		}
		for _, slice := range slices.Items {
			if owns(&slice, "Service", w.ServiceName, types.UID(o.ServiceUID)) {
				return true, nil
			}
		}
	}
	return false, nil
}

var _ workload.Executor = (*Executor)(nil)
