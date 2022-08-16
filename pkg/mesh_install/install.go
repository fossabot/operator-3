package mesh_install

import (
	"fmt"
	"github.com/greymatter-io/operator/pkg/cuemodule"
	"github.com/greymatter-io/operator/pkg/gmapi"
	"github.com/greymatter-io/operator/pkg/k8sapi"
	"github.com/greymatter-io/operator/pkg/wellknown"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ApplyMesh installs and updates Grey Matter core components and dependencies for a single mesh.
func (i *Installer) ApplyMesh() {
	freshLoadOperatorCUE, mesh, err := cuemodule.LoadAll(i.CueRoot)
	if err != nil {
		logger.Error(err, "failed to load CUE during Apply")
		return
	}
	meshInitialInstall := i.Mesh == nil
	i.Mesh = mesh

	i.OperatorCUE = freshLoadOperatorCUE

	// Create Namespace and image pull secret if this Mesh is new.
	if meshInitialInstall {
		logger.Info("Installing Mesh", "Name", mesh)
		namespace := &v1.Namespace{
			TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name: mesh.Spec.InstallNamespace,
			},
		}
		k8sapi.Apply(i.K8sClient, namespace, nil, k8sapi.GetOrCreate)
		secret := i.imagePullSecret.DeepCopy()
		secret.Namespace = mesh.Spec.InstallNamespace

		if i.Config.AutoCopyImagePullSecret {
			k8sapi.Apply(i.K8sClient, secret, i.owner, k8sapi.GetOrCreate)
		} else {
			err := k8sapi.Apply(i.K8sClient, secret, i.owner, k8sapi.Get)
			if err != nil {
				logger.Info("imagePullSecret not found in Core Mesh namespace", "AutoCopyImagePullSecret", i.Config.AutoCopyImagePullSecret, "Mesh Namespace", mesh.Spec.InstallNamespace)
			}
		}
	} else {
		logger.Info("Updating Mesh", "Name", mesh)
	}

	for _, watchedNS := range mesh.Spec.WatchNamespaces {
		// Create all watched namespaces, if they don't already exist
		namespace := &v1.Namespace{
			TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name: watchedNS,
			},
		}

		k8sapi.Apply(i.K8sClient, namespace, i.owner, k8sapi.GetOrCreate)
		// Copy the imagePullSecret into all watched namespaces
		secret := i.imagePullSecret.DeepCopy()
		secret.Namespace = watchedNS

		if i.Config.AutoCopyImagePullSecret {
			k8sapi.Apply(i.K8sClient, secret, i.owner, k8sapi.GetOrCreate)
			logger.Info("imagePullSecret found or created", "AutoCopyImagePullSecret", i.Config.AutoCopyImagePullSecret, "WatchNamespace", watchedNS)
		} else {
			err := k8sapi.Apply(i.K8sClient, secret, i.owner, k8sapi.Get)
			if err != nil {
				logger.Info("imagePullSecret not found in watched namespace", "AutoCopyImagePullSecret", i.Config.AutoCopyImagePullSecret, "WatchNamespace", watchedNS)
			}
		}
	}

	// Extract 'em
	manifestObjects, err := i.OperatorCUE.ExtractCoreK8sManifests()
	if err != nil {
		logger.Error(err, "failed to extract k8s manifests")
		return
	}

	// Remove anything from the list that hasn't changed since the last known update
	changedManifestObjects, deletedManifestObjects := i.Sync.SyncState.FilterChangedK8s(manifestObjects)
	// Apply the changed k8s manifests
	logger.Info("Applying updated Kubernetes manifests, if any")
	for _, manifest := range changedManifestObjects {
		logger.Info("Applying manifest:",
			"Name", manifest.GetName(),
			"Repr", manifest)

		k8sapi.Apply(i.K8sClient, manifest, i.owner, k8sapi.CreateOrUpdate)
	}
	// And delete the deleted ones
	k8sapi.DeleteAll(i.K8sClient, deletedManifestObjects)

	if meshInitialInstall {
		i.ConfigureMeshClient(mesh, i.Sync) // Synchronously applies the Grey Matter configuration once Control and Catalog are up
	} else {
		logger.Info("Applying updated mesh configs, if any")
		i.EnsureClient("ApplyMesh")
		go gmapi.ApplyCoreMeshConfigs(i.Client, i.OperatorCUE)
	}
}

func AddClusterLabels(tmpl v1.PodTemplateSpec, meshName, clusterName string) v1.PodTemplateSpec {
	if tmpl.Labels == nil {
		tmpl.Labels = make(map[string]string)
	}
	// For service discovery
	tmpl.Labels[wellknown.LABEL_CLUSTER] = clusterName
	// For Spire identification
	tmpl.Labels[wellknown.LABEL_WORKLOAD] = fmt.Sprintf("%s.%s", meshName, clusterName)
	return tmpl
}
