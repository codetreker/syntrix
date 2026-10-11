package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/provisioning/config"
	"github.com/codetreker/syntrix/console-server/internal/workload"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	appsFake "k8s.io/client-go/kubernetes/typed/apps/v1/fake"
	coreFake "k8s.io/client-go/kubernetes/typed/core/v1/fake"
	discoveryFake "k8s.io/client-go/kubernetes/typed/discovery/v1/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testConfig() config.Config {
	c := config.DefaultConfig()
	c.Mode = "kubeconfig"
	c.Kubeconfig = "unused"
	c.Context = "development"
	c.Namespace = "test-services"
	c.InstallationID = "console-dev"
	return c
}

func testWorkload() workload.Workload {
	id := strings.Repeat("a", 32)
	template := workload.Template{ID: "http", Image: "nginxinc/nginx-unprivileged:1.27.5-alpine", Port: 8080, Env: map[string]string{"ENVIRONMENT": "test"}, CPURequest: "25m", CPULimit: "100m", MemoryRequest: "32Mi", MemoryLimit: "64Mi", ProbePath: "/"}
	snapshot, _ := json.Marshal(template)
	digest := sha256.Sum256(snapshot)
	return workload.Workload{ID: id, DeveloperID: "dev-alice", TemplateID: template.ID, Snapshot: template, TemplateDigest: hex.EncodeToString(digest[:]), Namespace: "test-services", InstallationID: "console-dev", DesiredState: workload.DesiredRunning, DeploymentName: "w-" + id, ServiceName: "w-" + id}
}

func testExecutor(t *testing.T, objects ...runtime.Object) (*Executor, *k8stesting.Fake, k8stesting.ObjectTracker) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, discoveryv1.AddToScheme(scheme))
	tracker := k8stesting.NewObjectTracker(scheme, nil)
	for _, obj := range objects {
		require.NoError(t, tracker.Add(obj))
	}
	f := &k8stesting.Fake{}
	f.AddReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject()
		meta := obj.(metav1.Object)
		meta.SetUID(types.UID(action.GetResource().Resource + "-uid"))
		meta.SetGeneration(1)
		return false, nil, nil
	})
	f.AddReactor("*", "*", k8stesting.ObjectReaction(tracker))
	e := &Executor{cfg: testConfig(), apps: &appsFake.FakeAppsV1{Fake: f}, core: &coreFake.FakeCoreV1{Fake: f}, discovery: &discoveryFake.FakeDiscoveryV1{Fake: f}}
	return e, f, tracker
}

func existingResources(t *testing.T, w workload.Workload) (*appsv1.Deployment, *corev1.Service) {
	t.Helper()
	d, err := deploymentFor(w)
	require.NoError(t, err)
	d.UID = "deployment-uid"
	d.Generation = 2
	d.Spec.Template.Spec.DeprecatedServiceAccount = d.Spec.Template.Spec.ServiceAccountName
	s := serviceFor(w, d.UID)
	s.UID = "service-uid"
	s.Spec.ClusterIP = "10.0.0.1"
	s.Spec.ClusterIPs = []string{"10.0.0.1"}
	s.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	s.Spec.IPFamilyPolicy = ptr(corev1.IPFamilyPolicySingleStack)
	return d, s
}

func TestEnsureCreatesImmutableResources(t *testing.T) {
	w := testWorkload()
	e, f, tracker := testExecutor(t)
	o, err := e.Ensure(context.Background(), w)
	require.NoError(t, err)
	require.Equal(t, "deployments-uid", o.DeploymentUID)
	require.Equal(t, "services-uid", o.ServiceUID)
	require.False(t, o.Ready)
	require.Equal(t, "waiting_for_deployment", o.Reason)
	dObj, err := tracker.Get(appsv1.SchemeGroupVersion.WithResource("deployments"), w.Namespace, w.DeploymentName)
	require.NoError(t, err)
	d := dObj.(*appsv1.Deployment)
	require.False(t, *d.Spec.Template.Spec.AutomountServiceAccountToken)
	require.True(t, *d.Spec.Template.Spec.SecurityContext.RunAsNonRoot)
	c := d.Spec.Template.Spec.Containers[0]
	require.False(t, *c.SecurityContext.Privileged)
	require.False(t, *c.SecurityContext.AllowPrivilegeEscalation)
	require.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
	require.Empty(t, d.Spec.Template.Spec.Volumes)
	require.Equal(t, w.Snapshot.Image, c.Image)
	require.Equal(t, w.Snapshot.Env["ENVIRONMENT"], c.Env[0].Value)
	w.DeploymentUID, w.ServiceUID = o.DeploymentUID, o.ServiceUID
	_, err = e.Ensure(context.Background(), w)
	require.NoError(t, err)
	creates := 0
	for _, action := range f.Actions() {
		if action.GetVerb() == "create" {
			creates++
		}
	}
	require.Equal(t, 2, creates)
}

func TestEnsureRejectsOwnershipAndSpecChanges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*appsv1.Deployment, *corev1.Service, *workload.Workload)
	}{
		{"deployment_uid", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) { w.DeploymentUID = "older-uid" }},
		{"service_uid", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) { w.ServiceUID = "older-uid" }},
		{"developer", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			d.Annotations[developerAnnotation] = "dev-bob"
		}},
		{"installation", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			d.Labels[installationLabel] = "another"
		}},
		{"image", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			d.Spec.Template.Spec.Containers[0].Image = "foreign:1"
		}},
		{"privileged", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			d.Spec.Template.Spec.Containers[0].SecurityContext.Privileged = ptr(true)
		}},
		{"digest", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			d.Annotations[digestAnnotation] = "other"
		}},
		{"service_selector", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			s.Spec.Selector[workloadLabel] = "foreign"
		}},
		{"node_port", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			s.Spec.Type = corev1.ServiceTypeNodePort
		}},
		{"service_owner", func(d *appsv1.Deployment, s *corev1.Service, w *workload.Workload) {
			s.OwnerReferences[0].UID = "foreign"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := testWorkload()
			d, s := existingResources(t, w)
			tt.mutate(d, s, &w)
			e, f, _ := testExecutor(t, d, s)
			o, err := e.Ensure(context.Background(), w)
			require.Error(t, err)
			require.False(t, o.Ready)
			for _, a := range f.Actions() {
				require.NotContains(t, []string{"create", "update", "patch", "delete"}, a.GetVerb())
			}
		})
	}
}

func TestEnsurePinsMissingResources(t *testing.T) {
	w := testWorkload()
	w.DeploymentUID = "lost-uid"
	e, f, _ := testExecutor(t)
	_, err := e.Ensure(context.Background(), w)
	require.EqualError(t, err, "kubernetes_deployment_missing")
	require.Len(t, f.Actions(), 1)
	w = testWorkload()
	w.ServiceUID = "lost-service"
	d, _ := existingResources(t, w)
	e, f, _ = testExecutor(t, d)
	o, err := e.Ensure(context.Background(), w)
	require.EqualError(t, err, "kubernetes_service_missing")
	require.Equal(t, string(d.UID), o.DeploymentUID)
	for _, a := range f.Actions() {
		require.NotEqual(t, "create", a.GetVerb())
	}
}

func TestEnsureRecoversAlreadyExists(t *testing.T) {
	w := testWorkload()
	d, s := existingResources(t, w)
	e, f, tracker := testExecutor(t)
	for _, pair := range []struct {
		resource string
		object   runtime.Object
	}{{"deployments", d}, {"services", s}} {
		pair := pair
		f.PrependReactor("create", pair.resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
			require.NoError(t, tracker.Add(pair.object))
			return true, nil, apierrors.NewAlreadyExists(a.GetResource().GroupResource(), w.DeploymentName)
		})
	}
	o, err := e.Ensure(context.Background(), w)
	require.NoError(t, err)
	require.Equal(t, string(d.UID), o.DeploymentUID)
	require.Equal(t, string(s.UID), o.ServiceUID)
}

func TestEnsureRecoversCommittedTimeout(t *testing.T) {
	w := testWorkload()
	d, s := existingResources(t, w)
	e, f, tracker := testExecutor(t)
	f.PrependReactor("create", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		require.NoError(t, tracker.Add(d))
		return true, nil, context.DeadlineExceeded
	})
	o, err := e.Ensure(context.Background(), w)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, o.DeploymentUID)
	require.NoError(t, tracker.Add(s))
	o, err = e.Ensure(context.Background(), w)
	require.NoError(t, err)
	require.Equal(t, string(d.UID), o.DeploymentUID)
	creates := 0
	for _, action := range f.Actions() {
		if action.GetVerb() == "create" {
			creates++
		}
	}
	require.Equal(t, 1, creates)
}

func TestEnsurePropagatesReadFailures(t *testing.T) {
	w := testWorkload()
	e, f, _ := testExecutor(t)
	cause := apierrors.NewTimeoutError("private-value", 1)
	f.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
	o, err := e.Ensure(context.Background(), w)
	require.ErrorIs(t, err, cause)
	require.Equal(t, "kubernetes_deployment_ensure_timeout", o.Reason)
	require.False(t, o.Ready)
}

func TestEnsureReturnsPartialIdentityAndSafeErrors(t *testing.T) {
	w := testWorkload()
	e, f, _ := testExecutor(t)
	cause := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, w.ServiceName, errors.New("secret=private-value"))
	f.PrependReactor("create", "services", func(a k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
	o, err := e.Ensure(context.Background(), w)
	require.ErrorIs(t, err, cause)
	require.Equal(t, "deployments-uid", o.DeploymentUID)
	require.Empty(t, o.ServiceUID)
	require.Equal(t, "kubernetes_service_ensure_access_denied", o.Reason)
	require.NotContains(t, err.Error(), "private-value")
	var categorized interface{ ErrorCategory() string }
	require.ErrorAs(t, err, &categorized)
	require.Equal(t, o.Reason, categorized.ErrorCategory())
	require.NotContains(t, categorized.ErrorCategory(), "private-value")
}

func readyObjects(t *testing.T, w workload.Workload) []runtime.Object {
	d, s := existingResources(t, w)
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: w.DeploymentName + "-rs", Namespace: w.Namespace, UID: "rs-uid", Labels: labelsFor(w), Annotations: annotationsFor(w), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: ptr(true)}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: w.DeploymentName + "-pod", Namespace: w.Namespace, UID: "pod-uid", Labels: labelsFor(w), Annotations: annotationsFor(w), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr(true)}}}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: w.ServiceName + "-endpoints", Namespace: w.Namespace, UID: "slice-uid", Labels: map[string]string{discoveryv1.LabelServiceName: s.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: s.Name, UID: s.UID, Controller: ptr(true)}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: ptr("http"), Port: ptr(w.Snapshot.Port), Protocol: ptr(corev1.ProtocolTCP)}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.5"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(true)}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: w.Namespace, Name: pod.Name, UID: pod.UID}}}}
	return []runtime.Object{d, s, rs, pod, slice}
}

func TestReadyRequiresOwnedCurrentDeploymentAndEndpoints(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func([]runtime.Object)
	}{
		{"ready", func(o []runtime.Object) {}},
		{"stale_generation", func(o []runtime.Object) { o[0].(*appsv1.Deployment).Status.ObservedGeneration = 1 }},
		{"stale_replicas", func(o []runtime.Object) { o[0].(*appsv1.Deployment).Status.UpdatedReplicas = 0 }},
		{"no_available", func(o []runtime.Object) { o[0].(*appsv1.Deployment).Status.AvailableReplicas = 0 }},
		{"foreign_slice", func(o []runtime.Object) { o[4].(*discoveryv1.EndpointSlice).OwnerReferences[0].UID = "another" }},
		{"endpoint_not_ready", func(o []runtime.Object) { o[4].(*discoveryv1.EndpointSlice).Endpoints[0].Conditions.Ready = ptr(false) }},
		{"endpoint_terminating", func(o []runtime.Object) {
			o[4].(*discoveryv1.EndpointSlice).Endpoints[0].Conditions.Terminating = ptr(true)
		}},
		{"endpoint_wrong_port", func(o []runtime.Object) { o[4].(*discoveryv1.EndpointSlice).Ports[0].Port = ptr(int32(90)) }},
		{"pod_replaced", func(o []runtime.Object) { o[3].(*corev1.Pod).UID = "replacement" }},
		{"pod_label", func(o []runtime.Object) { o[3].(*corev1.Pod).Labels[workloadLabel] = "different" }},
		{"pod_annotation", func(o []runtime.Object) { o[3].(*corev1.Pod).Annotations[developerAnnotation] = "dev-bob" }},
		{"replicaset_replaced", func(o []runtime.Object) { o[2].(*appsv1.ReplicaSet).UID = "replacement" }},
		{"foreign_deployment", func(o []runtime.Object) { o[2].(*appsv1.ReplicaSet).OwnerReferences[0].UID = "other-deployment" }},
		{"running_not_ready", func(o []runtime.Object) {
			p := o[3].(*corev1.Pod)
			p.Status.Phase = corev1.PodRunning
			p.Status.Conditions = nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := testWorkload()
			objects := readyObjects(t, w)
			tt.mutate(objects)
			e, _, _ := testExecutor(t, objects...)
			o, err := e.Ensure(context.Background(), w)
			require.NoError(t, err)
			require.Equal(t, tt.name == "ready", o.Ready)
		})
	}
}

func TestDeleteUsesUIDPreconditionsAndWaits(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	d, s := existingResources(t, w)
	e, f, _ := testExecutor(t, d, s)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.False(t, o.Deleted)
	require.Equal(t, string(d.UID), o.DeploymentUID)
	require.Equal(t, string(s.UID), o.ServiceUID)
	deletes := 0
	for _, action := range f.Actions() {
		if action.GetVerb() == "delete" {
			deletes++
			opts := action.(k8stesting.DeleteAction).GetDeleteOptions()
			require.NotNil(t, opts.Preconditions)
			require.NotNil(t, opts.Preconditions.UID)
			if action.GetResource().Resource == "deployments" {
				require.Equal(t, d.UID, *opts.Preconditions.UID)
				require.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
			} else {
				require.Equal(t, s.UID, *opts.Preconditions.UID)
			}
		}
	}
	require.Equal(t, 2, deletes)
	w.DeploymentUID, w.ServiceUID = o.DeploymentUID, o.ServiceUID
	o, err = e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.True(t, o.Deleted)
}

func TestDeleteRejectsReplacementBeforeMutation(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	w.DeploymentUID = "previous"
	d, s := existingResources(t, w)
	e, f, _ := testExecutor(t, d, s)
	o, err := e.Delete(context.Background(), w)
	require.Error(t, err)
	require.False(t, o.Deleted)
	for _, a := range f.Actions() {
		require.NotEqual(t, "delete", a.GetVerb())
	}
}

func TestDeleteRejectsServiceReplacementAndConflict(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	w.ServiceUID = "previous-service"
	d, s := existingResources(t, w)
	e, f, _ := testExecutor(t, d, s)
	_, err := e.Delete(context.Background(), w)
	require.Error(t, err)
	for _, a := range f.Actions() {
		require.NotEqual(t, "delete", a.GetVerb())
	}
	w.ServiceUID = string(s.UID)
	e, f, _ = testExecutor(t, d, s)
	cause := apierrors.NewConflict(schema.GroupResource{Resource: "services"}, s.Name, errors.New("UID precondition failed"))
	f.PrependReactor("delete", "services", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
	o, err := e.Delete(context.Background(), w)
	require.ErrorIs(t, err, cause)
	require.Equal(t, "kubernetes_service_delete_conflict", o.Reason)
	for _, a := range f.Actions() {
		if a.GetVerb() == "delete" {
			require.Equal(t, "services", a.GetResource().Resource)
		}
	}
}

func TestDeleteDoesNotRemoveFinalizers(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	d, s := existingResources(t, w)
	now := metav1.Now()
	d.DeletionTimestamp, s.DeletionTimestamp = &now, &now
	d.Finalizers, s.Finalizers = []string{"other/controller"}, []string{"other/controller"}
	e, f, _ := testExecutor(t, d, s)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.False(t, o.Deleted)
	for _, a := range f.Actions() {
		require.Equal(t, "get", a.GetVerb())
	}
}

func TestDeleteFindsDependentWithChangedLabels(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	w.DeploymentUID = "deployment-uid"
	objects := readyObjects(t, w)
	rs := objects[2].(*appsv1.ReplicaSet)
	rs.Labels = map[string]string{"changed": "true"}
	e, _, _ := testExecutor(t, rs)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.False(t, o.Deleted)
}

func TestDeleteWaitsForOrphanIdentityAndOwnerName(t *testing.T) {
	for _, tt := range []struct {
		name          string
		index         int
		eraseIdentity bool
	}{
		{"replicaset_identity", 2, false},
		{"replicaset_owner", 2, true},
		{"pod_owner", 3, true},
		{"endpoints_owner", 4, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := testWorkload()
			w.DesiredState = workload.DesiredDeleted
			objects := readyObjects(t, w)
			obj := objects[tt.index]
			if tt.eraseIdentity {
				meta := obj.(metav1.Object)
				meta.SetLabels(nil)
				meta.SetAnnotations(nil)
			}
			if tt.index == 4 {
				w.ServiceUID = "service-uid"
			}
			e, _, _ := testExecutor(t, obj)
			o, err := e.Delete(context.Background(), w)
			require.NoError(t, err)
			require.False(t, o.Deleted)
		})
	}
}

func TestDeleteIgnoresUnrelatedNamespaceResources(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	w.DeploymentUID = "deployment-uid"
	w.ServiceUID = "service-uid"
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: w.Namespace, UID: "other", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "other", UID: "other", Controller: ptr(true)}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: w.Namespace, UID: "other", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "other-rs", UID: "other-rs", Controller: ptr(true)}}}}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: w.Namespace, UID: "other", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "other", UID: "other", Controller: ptr(true)}}}}
	e, f, _ := testExecutor(t, rs, pod, slice)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.True(t, o.Deleted)
	for _, a := range f.Actions() {
		require.NotEqual(t, "delete", a.GetVerb())
	}
}

func TestDependencyObservationFailures(t *testing.T) {
	for _, tt := range []struct {
		name, resource, verb string
		deleting             bool
	}{
		{"read_endpoints", "endpointslices", "list", false},
		{"read_pod", "pods", "get", false},
		{"read_replicaset", "replicasets", "get", false},
		{"delete_deployment_read", "deployments", "get", true},
		{"delete_service_read", "services", "get", true},
		{"delete_replicasets_list", "replicasets", "list", true},
		{"delete_pods_list", "pods", "list", true},
		{"delete_endpoints_list", "endpointslices", "list", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := testWorkload()
			objects := readyObjects(t, w)
			if tt.deleting {
				w.DesiredState = workload.DesiredDeleted
				w.DeploymentUID = "deployment-uid"
				w.ServiceUID = "service-uid"
				objects = nil
			}
			e, f, _ := testExecutor(t, objects...)
			cause := errors.New("private-response-body")
			f.PrependReactor(tt.verb, tt.resource, func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
			var o workload.Observation
			var err error
			if tt.deleting {
				o, err = e.Delete(context.Background(), w)
			} else {
				o, err = e.Ensure(context.Background(), w)
			}
			require.ErrorIs(t, err, cause)
			require.NotContains(t, err.Error(), "private-response-body")
			require.False(t, o.Ready)
			require.False(t, o.Deleted)
		})
	}
}

func TestDeleteDeploymentFailurePreservesServiceIdentity(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	d, s := existingResources(t, w)
	e, f, _ := testExecutor(t, d, s)
	cause := context.DeadlineExceeded
	f.PrependReactor("delete", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, cause })
	o, err := e.Delete(context.Background(), w)
	require.ErrorIs(t, err, cause)
	require.Equal(t, string(d.UID), o.DeploymentUID)
	require.Equal(t, string(s.UID), o.ServiceUID)
	require.False(t, o.Deleted)
}

func TestEnsureReportsProgressDeadline(t *testing.T) {
	w := testWorkload()
	d, s := existingResources(t, w)
	d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "private-value"}}
	e, _, _ := testExecutor(t, d, s)
	o, err := e.Ensure(context.Background(), w)
	require.EqualError(t, err, "deployment_progress_deadline_exceeded")
	require.Equal(t, "deployment_progress_deadline_exceeded", o.Reason)
}

func TestIndependentWorkloadsWithIdenticalTemplateDoNotBlockDeletion(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	w.DeploymentUID = "deleted-deployment"
	w.ServiceUID = "deleted-service"
	other := testWorkload()
	other.ID = strings.Repeat("c", 32)
	other.DeploymentName = "w-" + other.ID
	other.ServiceName = "w-" + other.ID
	objects := readyObjects(t, other)
	e, f, _ := testExecutor(t, objects...)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.True(t, o.Deleted)
	for _, action := range f.Actions() {
		require.NotEqual(t, "delete", action.GetVerb())
	}
}

func TestEnsureRejectsTemplateCorruptionBeforeAPI(t *testing.T) {
	for _, tt := range []struct {
		name    string
		corrupt func(*workload.Workload)
	}{
		{"digest", func(w *workload.Workload) { w.TemplateDigest = "mismatch" }},
		{"quantity", func(w *workload.Workload) {
			w.Snapshot.CPURequest = "bad"
			raw, _ := json.Marshal(w.Snapshot)
			hash := sha256.Sum256(raw)
			w.TemplateDigest = hex.EncodeToString(hash[:])
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := testWorkload()
			tt.corrupt(&w)
			e, f, _ := testExecutor(t)
			_, err := e.Ensure(context.Background(), w)
			require.Error(t, err)
			require.Empty(t, f.Actions())
		})
	}
}

func TestDeleteRecoversServiceWhenDeploymentGone(t *testing.T) {
	w := testWorkload()
	w.DesiredState = workload.DesiredDeleted
	_, s := existingResources(t, w)
	e, _, _ := testExecutor(t, s)
	o, err := e.Delete(context.Background(), w)
	require.NoError(t, err)
	require.Equal(t, "deployment-uid", o.DeploymentUID)
	require.False(t, o.Deleted)
}

func TestDeleteWaitsForDependents(t *testing.T) {
	for _, index := range []int{2, 3, 4} {
		t.Run([]string{"replicaset", "pod", "endpoints"}[index-2], func(t *testing.T) {
			w := testWorkload()
			w.DesiredState = workload.DesiredDeleted
			w.DeploymentUID = "deployment-uid"
			w.ServiceUID = "service-uid"
			objects := readyObjects(t, w)
			e, _, _ := testExecutor(t, objects[index])
			o, err := e.Delete(context.Background(), w)
			require.NoError(t, err)
			require.False(t, o.Deleted)
			require.Equal(t, "waiting_for_dependent_deletion", o.Reason)
		})
	}
}

func TestFrozenTargetAndStateRejectBeforeAPI(t *testing.T) {
	for _, method := range []string{"ensure", "delete"} {
		t.Run(method, func(t *testing.T) {
			w := testWorkload()
			w.Namespace = "other"
			e, f, _ := testExecutor(t)
			var err error
			if method == "ensure" {
				_, err = e.Ensure(context.Background(), w)
			} else {
				_, err = e.Delete(context.Background(), w)
			}
			require.EqualError(t, err, "kubernetes_target_changed")
			require.Empty(t, f.Actions())
		})
	}
	w := testWorkload()
	w.ID = "bad"
	e, f, _ := testExecutor(t)
	_, err := e.Ensure(context.Background(), w)
	require.Error(t, err)
	require.Empty(t, f.Actions())
}

func TestClientConstructorRequiresExplicitContext(t *testing.T) {
	cfg := testConfig()
	cfg.Kubeconfig = filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(cfg.Kubeconfig, []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: https://127.0.0.1:65535\ncontexts:\n- name: development\n  context:\n    cluster: local\ncurrent-context: development\n"), 0600))
	e, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, e)
	cfg.Context = "absent"
	_, err = New(cfg)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "65535")
	cfg.Context = "development"
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(cfg.Kubeconfig), "token"), []byte("test-token"), 0600))
	require.NoError(t, os.WriteFile(cfg.Kubeconfig, []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: https://127.0.0.1:65535\ncontexts:\n- name: development\n  context:\n    cluster: local\n    user: developer\nusers:\n- name: developer\n  user:\n    tokenFile: token\n"), 0600))
	_, err = New(cfg)
	require.NoError(t, err)
	cfg.Kubeconfig = filepath.Join(t.TempDir(), "missing")
	_, err = New(cfg)
	require.Error(t, err)
	cfg.Mode = "implicit"
	_, err = New(cfg)
	require.Error(t, err)
}

func TestAPIFailureClassification(t *testing.T) {
	for _, tt := range []struct {
		cause  error
		suffix string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {apierrors.NewTimeoutError("secret", 1), "timeout"}, {apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "x", errors.New("secret")), "conflict"}, {errors.New("secret"), "failed"}} {
		err := apiFailure("read", tt.cause)
		require.EqualError(t, err, "kubernetes_read_"+tt.suffix)
		require.ErrorIs(t, err, tt.cause)
	}
}

func TestResourceQuantityErrors(t *testing.T) {
	w := testWorkload()
	for _, value := range []string{"invalid", "0", "500m"} {
		w.Snapshot.CPURequest = value
		_, err := deploymentFor(w)
		require.Error(t, err)
	}
}

func TestTimestampIsUTC(t *testing.T) {
	o := observation(testWorkload())
	require.Equal(t, time.UTC, o.ObservedAt.Location())
	require.WithinDuration(t, time.Now(), o.ObservedAt, time.Second)
}
