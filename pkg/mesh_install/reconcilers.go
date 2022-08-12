package mesh_install

import (
	"encoding/json"
	"github.com/greymatter-io/operator/pkg/gmapi"
	"github.com/greymatter-io/operator/pkg/k8sapi"
	"github.com/greymatter-io/operator/pkg/wellknown"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"reflect"
	"sort"
)

type podReconciler func(*corev1.Pod, *Installer)
type deploymentReconciler func(*appsv1.Deployment, *Installer)
type statefulsetReconciler func(*appsv1.StatefulSet, *Installer)

// TODO handle deletes
// TODO rename
func reconcileDeployment(deployment *appsv1.Deployment, i *Installer) {
	if hasLabels(deployment.Spec.Template) {
		return
	}
	logger.Info("reconciling deployment", "name", deployment.Name)
	deployment.Spec.Template = addLabels(deployment.Spec.Template, i.Mesh.Name, deployment.Name)
	annotations := deployment.Spec.Template.Annotations
	_, injectSidecar := annotations[wellknown.ANNOTATION_INJECT_SIDECAR_TO_PORT]
	if injectSidecar {
		go func() {
			i.ConfigureSidecar(i.OperatorCUE, deployment.Name, annotations)
		}()
	}
	k8sapi.Apply(i.K8sClient, deployment, i.owner, k8sapi.CreateOrUpdate)
}

// TODO handle deletes
// TODO rename
func reconcileStatefulSet(statefulset *appsv1.StatefulSet, i *Installer) {
	if hasLabels(statefulset.Spec.Template) {
		return
	}
	logger.Info("reconciling statefulset", "name", statefulset.Name)
	statefulset.Spec.Template = addLabels(statefulset.Spec.Template, i.Mesh.Name, statefulset.Name)
	annotations := statefulset.Spec.Template.Annotations
	_, injectSidecar := annotations[wellknown.ANNOTATION_INJECT_SIDECAR_TO_PORT]
	if injectSidecar {
		go func() {
			i.ConfigureSidecar(i.OperatorCUE, statefulset.Name, annotations)
		}()
	}

	k8sapi.Apply(i.K8sClient, statefulset, i.owner, k8sapi.CreateOrUpdate)
}

func injectSidecarPodReconciler(pod *corev1.Pod, i *Installer) {
	logger.Info("reconciling pod for sidecar injection", "name", pod.Name)
	annotations := pod.Annotations
	// Check if sidecar injection was requested
	if injectSidecarTo, injectSidecar := annotations[wellknown.ANNOTATION_INJECT_SIDECAR_TO_PORT]; !injectSidecar || injectSidecarTo == "" {
		logger.Info("no inject-sidecar-to annotation, skipping", "name", pod.Name, "annotations", annotations)
		return
	}
	// Check for a cluster label; if not found, this pod does not belong to a Mesh.
	clusterLabel, ok := pod.Labels[wellknown.LABEL_CLUSTER]
	if !ok {
		logger.Info("discovered pod has no cluster label - skipping sidecar injection", "name", pod.Name, "labels", pod.Labels)
		return
	}
	// Check for an existing proxy port; if found, this pod already has a sidecar.
	for _, container := range pod.Spec.Containers {
		for _, p := range container.Ports {
			// TODO don't hard-code this name. Pull from the same CUE Control uses to find these
			if p.Name == "proxy" {
				return
			}
		}
	}

	// Get a sidecar container and volumes to inject
	container, volumes, err := i.OperatorCUE.UnifyAndExtractSidecar(clusterLabel)
	if err != nil {
		logger.Error(err, "unable to extract sidecar injection configuration during, suspect bad CUE", "name", pod.Name, "clusterLabel", clusterLabel)
		return
	}

	pod.Spec.Containers = append(pod.Spec.Containers, container)
	pod.Spec.Volumes = append(pod.Spec.Volumes, volumes...)

	// Inject a reference to the image pull secret
	var hasImagePullSecret bool
	for _, secret := range pod.Spec.ImagePullSecrets {
		if secret.Name == "gm-docker-secret" {
			hasImagePullSecret = true
		}
	}
	if !hasImagePullSecret {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: "gm-docker-secret"})
	}

	logger.Info("injecting sidecar", "name", clusterLabel, "kind", "Pod", "generateName", pod.GenerateName+"*", "namespace", pod.Namespace)
	k8sapi.Apply(i.K8sClient, pod, i.owner, k8sapi.CreateOrUpdate)
}

// Reconciles the Redis Listener's list of allowable incoming connections (for Spire) from the list of live Pods
// with sidecars.
func reconcileSidecarListForRedisIngress(pod *corev1.Pod, i *Installer) {
	var redisListener json.RawMessage
	sidecarSet := make(map[string]struct{})
	for _, container := range pod.Spec.Containers {
		for _, p := range container.Ports {
			// TODO look for the annotations rather than the actual presence of a sidecar, since we'll be injecting based on that
			if p.Name == "proxy" {
				if pod.Labels == nil {
					pod.Labels = make(map[string]string)
				}
				if clusterName, ok := pod.Labels[wellknown.LABEL_CLUSTER]; ok {
					sidecarSet[clusterName] = struct{}{}
				}
			}
		}
	}
	var sidecarList []string
	for name := range sidecarSet {
		sidecarList = append(sidecarList, name)
	}
	sort.Strings(sidecarList)
	sort.Strings(i.Defaults.SidecarList)
	if len(sidecarList) == 0 || reflect.DeepEqual(sidecarList, i.Defaults.SidecarList) {
		return
	}
	logger.Info("The list of sidecars in the environment has changed. Updating Redis ingress for health checks.", "Updated List", sidecarList)
	i.Defaults.SidecarList = sidecarList
	tempOperatorCUE, err := i.OperatorCUE.TempGMValueUnifiedWithDefaults(i.Defaults)
	if err != nil {
		logger.Error(err,
			"error attempting to unify mesh after sidecarList update - this should never happen - check Mesh integrity",
			"Mesh", i.Mesh)
		return
	}
	redisListener, err = tempOperatorCUE.ExtractRedisListener()
	if err != nil {
		logger.Error(err,
			"error extracting redis_listener from CUE - ignoring",
			"Mesh", i.Mesh)
		return
	}
	if i.Client != nil {
		i.Client.ControlCmds <- gmapi.MkApply("listener", redisListener)
	}

}
