package cli

import (
	"context"
	"testing"
	"time"

	"github.com/greymatter-io/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestNewClient(t *testing.T) {
	t.Skip() // only test in dev, not in ci

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))

	ctx, cancel := context.WithCancel(context.Background())

	c, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}

	mesh := &v1alpha1.Mesh{
		ObjectMeta: metav1.ObjectMeta{Name: "mesh"},
		Spec: v1alpha1.MeshSpec{
			Zone:           "zone",
			ReleaseVersion: "1.7",
		},
	}

	c.configureMeshClient(
		mesh,
		mesh.Options(""),
		"--base64-config", mkCLIConfig(
			"http://localhost:5555",
			"http://localhost:8181",
			"mesh",
		),
	)

	containers := []corev1.Container{
		{Ports: []corev1.ContainerPort{
			{Name: "api", ContainerPort: 5555},
			{Name: "ui", ContainerPort: 3000},
		}},
	}

	c.ConfigureService("mesh", "mock", nil, containers)
	c.RemoveService("mesh", "mock", nil, containers)

	c.RemoveMeshClient("mesh")

	time.Sleep(time.Second * 5)
	cancel()
}

func TestCLIVersion(t *testing.T) {
	t.Skip() // only test in dev, not in ci

	v, err := cliversion()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(v)
}
