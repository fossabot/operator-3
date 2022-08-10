package gitops

import (
	"context"
	"testing"

	"github.com/greymatter-io/operator/pkg/cuemodule"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultZone      = "default-zone"
	defaultNamespace = "gm-operator"
)

func TestNewGMObjectRef(t *testing.T) {
	cases := map[string]struct {
		typ       string
		jsonBytes []byte
		expected  GMObjectRef
	}{
		"cluster": {
			"cluster",
			[]byte(`{"cluster_key": "grapefruit", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "cluster", ID: "grapefruit", Hash: 11431995707094721787},
		},
		"listener": {
			"listener",
			[]byte(`{"listener_key": "banana", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "listener", ID: "banana", Hash: 9137765789731928109},
		},
		"proxy": {
			"proxy",
			[]byte(`{"proxy_key": "kiwi", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "proxy", ID: "kiwi", Hash: 2560221913592643480},
		},
		"route": {
			"route",
			[]byte(`{"route_key": "strawberry", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "route", ID: "strawberry", Hash: 5208631178048549669},
		},
		"domain": {
			"domain",
			[]byte(`{"domain_key": "pineapple", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "domain", ID: "pineapple", Hash: 17031460684138845760},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewGMObjectRef(tc.jsonBytes, tc.typ)
			assert.Equal(t, tc.expected, *got)
		})
	}
}

func TestGMHashKey(t *testing.T) {
	cases := map[string]struct {
		typ      string
		object   []byte
		Ref      GMObjectRef
		expected string
	}{
		"cluster": {
			"cluster",
			[]byte(`{"cluster_key": "grapefruit", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "cluster", ID: "grapefruit", Hash: 11431995707094721787},
			"default-zone-cluster-grapefruit",
		},
		"listener": {
			"listener",
			[]byte(`{"listener_key": "banana", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "listener", ID: "banana", Hash: 9137765789731928109},
			"default-zone-listener-banana",
		},
		"proxy": {
			"proxy",
			[]byte(`{"proxy_key": "kiwi", "zone_key": "default-zone"}`),
			GMObjectRef{Zone: defaultZone, Kind: "proxy", ID: "kiwi", Hash: 2560221913592643480},
			"default-zone-proxy-kiwi",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewGMObjectRef(tc.object, tc.typ).HashKey()

			// NewGMObjectRef returns a pointer so we
			// dereference to assert the values are correct
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestNewK8sObjectRef(t *testing.T) {
	cases := map[string]struct {
		object   client.Object
		expected K8sObjectRef
	}{
		"deployment": {
			&appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: defaultNamespace,
				},
			},
			K8sObjectRef{
				Namespace: defaultNamespace,
				Name:      "test-deployment",
				Kind:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
				Hash:      16988695845095796670,
			},
		},
		"statefulset": {
			&appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Statefulset",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: defaultNamespace,
				},
			},
			K8sObjectRef{
				Namespace: defaultNamespace,
				Name:      "test-sts",
				Kind:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Statefulset"},
				Hash:      8522595552864184581,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewK8sObjectRef(tc.object)

			// NewGMObjectRef returns a pointer so we
			// dereference to assert the values are correct
			assert.Equal(t, tc.expected, *got)
		})
	}
}

func TestK8sHashKey(t *testing.T) {
	cases := map[string]struct {
		object   client.Object
		ref      K8sObjectRef
		expected string
	}{
		"deployment": {
			&appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: defaultNamespace,
				},
			},
			K8sObjectRef{
				Namespace: defaultNamespace,
				Name:      "test-deployment",
				Kind:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
				Hash:      16988695845095796670,
			},
			"gm-operator-apps/v1, Kind=Deployment-test-deployment",
		},
		"statefulset": {
			&appsv1.StatefulSet{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Statefulset",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: defaultNamespace,
				},
			},
			K8sObjectRef{
				Namespace: defaultNamespace,
				Name:      "test-sts",
				Kind:      schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Statefulset"},
				Hash:      8522595552864184581,
			},
			"gm-operator-apps/v1, Kind=Statefulset-test-sts",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewK8sObjectRef(tc.object).HashKey()

			// NewGMObjectRef returns a pointer so we
			// dereference to assert the values are correct
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestNewSyncState(t *testing.T) {
	// We should see an error message and empty sync state because we couldn't
	// connect to redis
	ss := NewSyncState(context.Background(), cuemodule.Defaults{})
	assert.Equal(t, &SyncState{}, ss)
}
