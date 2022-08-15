// Package mesh_install exposes functions for applying resources to a Kubernetes cluster.
// Its exposed functions receive a K8sClient for communicating with the cluster.
package mesh_install

import (
	"context"
	"fmt"
	"github.com/cloudflare/cfssl/csr"
	"github.com/greymatter-io/operator/pkg/wellknown"
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	"strings"
	"time"

	"github.com/greymatter-io/operator/api/v1alpha1"
	"github.com/greymatter-io/operator/pkg/cfsslsrv"
	"github.com/greymatter-io/operator/pkg/cuemodule"
	"github.com/greymatter-io/operator/pkg/gitops"
	"github.com/greymatter-io/operator/pkg/gmapi"
	"github.com/greymatter-io/operator/pkg/k8sapi"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	logger = ctrl.Log.WithName("mesh_install")
)

// Installer stores a map of version.Version and a distinct version.Sidecar for each mesh.
type Installer struct {
	*gmapi.CLI // Grey Matter CLI
	K8sClient  *client.Client

	cfssl *cfsslsrv.CFSSLServer

	// The meshes.greymatter.io CRD, used as an owner when applying cluster-scoped resources.
	// If the operator is uninstalled on a cluster, owned cluster-scoped resources will be cleaned up.
	owner *extv1.CustomResourceDefinition
	// The Docker image pull secret to create in namespaces where core services are installed.
	imagePullSecret *corev1.Secret

	// Container for THE mesh (on the way to an experimental 1:1 operator:mesh paradigm)
	// Contains the default after load
	Mesh *v1alpha1.Mesh

	// Container for all K8s and GM CUE cue.Values
	OperatorCUE *cuemodule.OperatorCUE

	// Root on disk of the operator CUE. Used for reloading the default configs on teardown
	CueRoot string

	// Operator config loadable from CUE
	Config cuemodule.Config

	// Select defaults that may be directly overridden from Go
	Defaults cuemodule.Defaults

	// Looked up on start
	clusterIngressDomain string

	// Sync configuration with access to a callback for updating on git repo changes
	Sync *gitops.Sync
}

// New returns a new *Installer instance for installing Grey Matter components and dependencies.
func New(c *client.Client, operatorCUE *cuemodule.OperatorCUE, initialMesh *v1alpha1.Mesh, cueRoot string, gmcli *gmapi.CLI, cfssl *cfsslsrv.CFSSLServer, sync *gitops.Sync) (*Installer, error) {
	config, defaults := operatorCUE.ExtractConfig()
	return &Installer{
		CLI:         gmcli,
		K8sClient:   c,
		cfssl:       cfssl,
		OperatorCUE: operatorCUE,
		Mesh:        initialMesh,
		CueRoot:     cueRoot,
		Config:      config,
		Defaults:    defaults,
		Sync:        sync,
	}, nil
}

// Start initializes resources and configurations after controller-manager has launched.
// It implements the controller-runtime Runnable interface.
func (i *Installer) Start(ctx context.Context) error {

	// Retrieve the operator image secret from the apiserver (block until it's retrieved).
	// This secret will be re-created in each install namespace and watch namespaces where core services are pulled.
	i.imagePullSecret = getImagePullSecret(i.K8sClient)

	// Get our Mesh CRD to set as an owner for cluster-scoped resources
	i.owner = &extv1.CustomResourceDefinition{}
	err := (*i.K8sClient).Get(ctx, client.ObjectKey{Name: "meshes.greymatter.io"}, i.owner)
	if err != nil {
		logger.Error(err, "Failed to get CustomResourceDefinition meshes.greymatter.io")
		return err
	}

	if i.Config.Spire {
		logger.Info("Attempting to apply spire server-ca secret")
		spireSecret := &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-ca",
				Namespace: "spire",
			},
		}
		spireSecret, err = injectGeneratedCertificates(spireSecret, i.cfssl)
		if err != nil {
			logger.Error(err, "Error while attempting to apply spire server-ca secret", "secret object", spireSecret)
			return err
		}
		k8sapi.Apply(i.K8sClient, spireSecret, i.owner, k8sapi.CreateOrUpdate)
	}

	// Try to get the OpenShift cluster ingress domain if it exists.
	clusterIngressDomain, ok := getOpenshiftClusterIngressDomain(i.K8sClient, i.Config.ClusterIngressName)
	if ok {
		// TODO: When not in OpenShift, check for other supported ingress class types such as Nginx or Voyager.
		// If no supported ingress types are found, just assume the user will configure ingress on their own.
		logger.Info("Identified OpenShift cluster domain name", "Domain", clusterIngressDomain)
		i.clusterIngressDomain = clusterIngressDomain
	}

	// If this operator's Mesh CR already exists in the environment, load it
	meshAlreadyDeployed := false
	meshList := &v1alpha1.MeshList{}
	if err := (*i.K8sClient).List(context.TODO(), meshList); err != nil {
		logger.Error(err, "failed to list all meshes for state restoration - check operator permissions")
	}
	for _, mesh := range meshList.Items {
		if mesh.Name == i.Mesh.Name {
			logger.Info("Mesh already deployed. Reloading values.", "Name", mesh.Name)
			i.Mesh = &mesh // load the live version of the mesh
			// immediately update OperatorCUE and the SidecarList
			err := i.OperatorCUE.UnifyWithMesh(i.Mesh)
			if err != nil {
				logger.Error(err,
					"error while attempting to unify existing deployed Mesh with Grey Matter mesh configs CUE",
					"Mesh", mesh)
				return err
			}
			i.ConfigureMeshClient(i.Mesh, i.Sync)
			meshAlreadyDeployed = true
			break
		}
	}

	// called on completion of a gitops sync cycle if there are new commits
	i.Sync.OnSyncCompleted = func() error {
		logger.Info("GitOps repo updated and synchronized. Reapplying configuration...")
		// reload CUE here
		_, freshLoadMesh, err := cuemodule.LoadAll(i.CueRoot)
		if err != nil {
			return err
		}
		// copy in old mesh dynamic values
		freshLoadMesh.TypeMeta = i.Mesh.TypeMeta
		i.Mesh.ObjectMeta.DeepCopyInto(&freshLoadMesh.ObjectMeta)

		i.ApplyMesh(i.Mesh, freshLoadMesh)

		return nil
	}

	// Immediately apply the default mesh from the CUE if the flag is set and we don't already have a mesh
	// Then re-apply the mesh whenever the repository is updated (checked by polling)
	go func() {
		// initial mesh application
		if i.Config.AutoApplyMesh && !meshAlreadyDeployed {
			logger.Info("Waiting 30 seconds to apply loaded default Mesh resource to cluster.")
			time.Sleep(30 * time.Second) // Sleep for an arbitrary initial duration
			for {
				err := k8sapi.Apply(i.K8sClient, i.Mesh, nil, k8sapi.GetOrCreate)
				if err == nil {
					break
				}
				logger.Info("Temporary failure to apply Mesh resource. Will retry in 10 seconds.")
				time.Sleep(10 * time.Second)
			}
		}

		// GitOps-triggered subsequent mesh applications
		i.Sync.Watch() // Executes its callback (defined above) whenever there are new commits
	}()

	/////////////////////
	// RECONCILERS SETUP
	/////////////////////
	var podReconcilers []podReconciler
	// If Spire, set up to periodically reconcile the extant sidecars with the Redis listener's allowable subjects
	if i.Config.Spire {
		podReconcilers = append(podReconcilers, reconcileSidecarListForRedisIngress)
	}
	go i.reconciliationDispatchLoop(
		podReconcilers,
		[]deploymentReconciler{reconcileDeploymentLabels, reconcileDeploymentSidecarInjection},
		[]statefulsetReconciler{reconcileStatefulSetLabels})

	return nil
}

// reconciliationDispatchLoop periodically queries K8s for Pods, Deployments, and StatefulSets, and dispatches to
// registered reconcilers to update those resources as necessary
func (i *Installer) reconciliationDispatchLoop(
	podReconcilers []podReconciler,
	deploymentReconcilers []deploymentReconciler,
	statefulsetReconcilers []statefulsetReconciler) {
	logger.Info("Beginning reconciliation loop for Pods, Deployments, and StatefulSets in watched namespaces")

ReconciliationLoop:
	for {
		time.Sleep(30 * time.Second)
		i.RLock()
		// List all pods anywhere
		pods := &corev1.PodList{}
		deployments := &appsv1.DeploymentList{}
		statefulsets := &appsv1.StatefulSetList{}
		for _, watchedNamespace := range i.Mesh.Spec.WatchNamespaces {

			// Only look in watchedNamespace
			opts := []client.ListOption{
				client.InNamespace(watchedNamespace),
			}

			// Find pods and dispatch to podReconcilers
			err := (*i.K8sClient).List(context.TODO(), pods, opts...)
			if err != nil {
				logger.Error(err, "failed to list pods for reconciliation", "namespace", watchedNamespace)
			}
			for _, pod := range pods.Items {
				for _, reconciler := range podReconcilers {
					reconciler(&pod, i)
				}
			}

			// Find deployments and dispatch to deploymentReconcilers
			err = (*i.K8sClient).List(context.TODO(), deployments, opts...)
			if err != nil {
				logger.Error(err, "failed to list pods for reconciliation", "namespace", watchedNamespace)
			}
			for _, deployment := range deployments.Items {

				for _, reconciler := range deploymentReconcilers {
					reconciler(&deployment, i)
				}
			}

			// Find statefulsets and dispatch to statefulsetReconcilers
			err = (*i.K8sClient).List(context.TODO(), statefulsets, opts...)
			if err != nil {
				logger.Error(err, "failed to list pods for reconciliation", "namespace", watchedNamespace)
			}
			for _, statefulset := range statefulsets.Items {
				for _, reconciler := range statefulsetReconcilers {
					reconciler(&statefulset, i)
				}
			}
		}

		if i.Client != nil {
			select {
			case <-i.Client.Ctx.Done():
				logger.Info("greymatter client context cancelled - stopping reconciliation dispatch loop",
					"error", i.Client.Ctx.Err())
				i.RUnlock()
				break ReconciliationLoop
			default:
			}
		}
		i.RUnlock()
	}
}

func hasLabels(tmpl corev1.PodTemplateSpec) bool {
	if tmpl.Labels == nil {
		tmpl.Labels = make(map[string]string)
	}
	_, ok := tmpl.Labels[wellknown.LABEL_WORKLOAD]
	return ok
}
func addLabels(tmpl corev1.PodTemplateSpec, meshName, clusterName string) corev1.PodTemplateSpec {
	if tmpl.Labels == nil {
		tmpl.Labels = make(map[string]string)
	}
	// For service discovery
	tmpl.Labels[wellknown.LABEL_CLUSTER] = clusterName
	// For Spire identification
	tmpl.Labels[wellknown.LABEL_WORKLOAD] = fmt.Sprintf("%s.%s", meshName, clusterName)
	return tmpl
}

// Retrieves the image pull secret in the gm-operator namespace.
// This retries indefinitely at 30s intervals and will block by design.
func getImagePullSecret(c *client.Client) *corev1.Secret {
	key := client.ObjectKey{Name: "gm-docker-secret", Namespace: "gm-operator"}
	operatorSecret := &corev1.Secret{}
	for operatorSecret.CreationTimestamp.IsZero() {
		if err := (*c).Get(context.TODO(), key, operatorSecret); err != nil {
			logger.Error(err, "No 'gm-docker-secret' image pull secret found in gm-operator namespace. Will retry in 30s.")
			time.Sleep(time.Second * 30)
		}
	}

	// Return new secret with just the dockercfgjson (without additional metadata).
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gm-docker-secret"},
		Type:       operatorSecret.Type,
		Data:       operatorSecret.Data,
	}
}

func injectGeneratedCertificates(secret *corev1.Secret, cs *cfsslsrv.CFSSLServer) (*corev1.Secret, error) {
	root := cs.GetRootCA()
	ca, caKey, err := cs.RequestIntermediateCA(csr.CertificateRequest{
		CN:         "Grey Matter SPIFFE Intermediate CA",
		KeyRequest: &csr.KeyRequest{A: "ecdsa", S: 256},
		Names: []csr.Name{
			{C: "US", ST: "VA", L: "Alexandria", O: "Grey Matter"},
		},
	})
	if err != nil {
		return nil, err
	}

	secret.StringData = map[string]string{
		"root.crt":         string(root),
		"intermediate.crt": strings.Join([]string{string(ca), string(root)}, "\n"),
		"intermediate.key": string(caKey),
	}

	return secret, nil
}

func getOpenshiftClusterIngressDomain(c *client.Client, ingressName string) (string, bool) {
	clusterIngressList := &configv1.IngressList{}
	if err := (*c).List(context.TODO(), clusterIngressList); err != nil {
		return "", false
	} else {
		for _, i := range clusterIngressList.Items {
			if i.Name == ingressName {
				return i.Spec.Domain, true
			}
		}
	}
	return "", false
}

// Check that a suported ingress controller class exists in a kubernetes cluster.
// This will be expanded later on as we support additional ingress implementations.
//lint:ignore U1000 save for reference
func isSupportedKubernetesIngressClassPresent(c client.Client) bool {
	ingressClassList := &networkingv1.IngressClassList{}
	if err := c.List(context.TODO(), ingressClassList); err != nil {
		return false
	}
	for _, i := range ingressClassList.Items {
		switch i.Spec.Controller {
		case "nginx", "voyager":
			return true
		}
	}
	return false
}
