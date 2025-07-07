package controller_test

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "github.com/nebius/helmrelease-trigger-operator/internal/controller"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestExtractFromAnnotations(t *testing.T) {

	tests := []struct {
		name           string
		annotations    map[string]string
		expectedHRName []string
		expectedNS     string
		expectedDigest string
	}{
		{
			name: "All annotations present",
			annotations: map[string]string{
				HRNSAnnotation:   "custom-namespace",
				HRNameAnnotation: "hr1,hr2,hr3",
				HashAnnotation:   "digest123",
			},
			expectedHRName: []string{"hr1", "hr2", "hr3"},
			expectedNS:     "custom-namespace",
			expectedDigest: "digest123",
		},
		{
			name: "Missing HRNSAnnotation",
			annotations: map[string]string{
				HRNameAnnotation: "hr1,hr2",
				HashAnnotation:   "digest456",
			},
			expectedHRName: []string{"hr1", "hr2"},
			expectedNS:     DefaultFluxcdNamespace,
			expectedDigest: "digest456",
		},
		{
			name: "Missing HRNameAnnotation",
			annotations: map[string]string{
				HRNSAnnotation: "custom-namespace",
				HashAnnotation: "digest789",
			},
			expectedHRName: []string{""},
			expectedNS:     "custom-namespace",
			expectedDigest: "digest789",
		},
		{
			name:           "No annotations",
			annotations:    map[string]string{},
			expectedHRName: []string{""},
			expectedNS:     DefaultFluxcdNamespace,
			expectedDigest: "",
		},
		{
			name: "HRNameAnnotation with single value",
			annotations: map[string]string{
				HRNSAnnotation:   "custom-namespace",
				HRNameAnnotation: "hr1",
				HashAnnotation:   "digest000",
			},
			expectedHRName: []string{"hr1"},
			expectedNS:     "custom-namespace",
			expectedDigest: "digest000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			resource := &unstructured.Unstructured{}
			resource.SetAnnotations(tt.annotations)

			r := &BaseReconciler{}

			// Act
			listHRName, hrNamespace, oldDigest := r.ExtractFromAnnotations(resource)

			// Assert
			assert.Equal(t, tt.expectedHRName, listHRName)
			assert.Equal(t, tt.expectedNS, hrNamespace)
			assert.Equal(t, tt.expectedDigest, oldDigest)
		})
	}
}

func TestGetOrGenerateAnnotations(t *testing.T) {
	r := &BaseReconciler{}

	tests := []struct {
		name                string
		existingAnnotations map[string]string
		hrNameAnnotation    string
		ns                  string
		hrName              string
		expectedAnnotations map[string]string
	}{
		{
			name:                "No existing annotations",
			existingAnnotations: nil,
			hrNameAnnotation:    HRNameAnnotation,
			ns:                  "namespace1",
			hrName:              "hr1",
			expectedAnnotations: map[string]string{
				HRNameAnnotation: "hr1",
				HRNSAnnotation:   "namespace1",
				HashAnnotation:   "dccb0d0c",
			},
		},
		{
			name: "Existing annotations without HRNameAnnotation",
			existingAnnotations: map[string]string{
				HRNSAnnotation: "namespace1",
			},
			hrNameAnnotation: HRNameAnnotation,
			ns:               "namespace2",
			hrName:           "hr2",
			expectedAnnotations: map[string]string{
				HRNameAnnotation: "hr2",
				HRNSAnnotation:   "namespace2",
				HashAnnotation:   "dccb0d0c",
			},
		},
		{
			name: "Existing HRNameAnnotation without duplicate",
			existingAnnotations: map[string]string{
				HRNameAnnotation: "hr1",
				HRNSAnnotation:   "namespace1",
			},
			hrNameAnnotation: HRNameAnnotation,
			ns:               "namespace1",
			hrName:           "hr2",
			expectedAnnotations: map[string]string{
				HRNameAnnotation: "hr1,hr2",
				HRNSAnnotation:   "namespace1",
				HashAnnotation:   "dccb0d0c",
			},
		},
		{
			name: "Existing HRNameAnnotation with duplicate",
			existingAnnotations: map[string]string{
				HRNameAnnotation: "hr1",
				HRNSAnnotation:   "namespace1",
			},
			hrNameAnnotation: HRNameAnnotation,
			ns:               "namespace1",
			hrName:           "hr1",
			expectedAnnotations: map[string]string{
				HRNameAnnotation: "hr1",
				HRNSAnnotation:   "namespace1",
				HashAnnotation:   "dccb0d0c",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-resource",
				},
				Data: map[string]string{
					"key": "value",
				},
			}
			resource.SetAnnotations(tt.existingAnnotations)

			updatedAnnotations := r.GetOrGenerateAnnotations(resource, tt.hrNameAnnotation, tt.ns, tt.hrName)

			assert.Equal(t, tt.expectedAnnotations, updatedAnnotations)
		})
	}
}

func TestAddAnnotationsAndLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &BaseReconciler{
		Client: fakeClient,
	}

	tests := []struct {
		name                string
		existingAnnotations map[string]string
		existingLabels      map[string]string
		newAnnotations      map[string]string
		newLabels           map[string]string
		expectedAnnotations map[string]string
		expectedLabels      map[string]string
	}{
		{
			name:                "Add new annotations and labels",
			existingAnnotations: nil,
			existingLabels:      nil,
			newAnnotations:      map[string]string{"annotation1": "value1"},
			newLabels:           map[string]string{"label1": "value1"},
			expectedAnnotations: map[string]string{"annotation1": "value1"},
			expectedLabels:      map[string]string{"label1": "value1"},
		},
		{
			name:                "Update existing annotations and labels",
			existingAnnotations: map[string]string{"annotation1": "oldValue"},
			existingLabels:      map[string]string{"label1": "oldValue"},
			newAnnotations:      map[string]string{"annotation1": "newValue"},
			newLabels:           map[string]string{"label1": "newValue"},
			expectedAnnotations: map[string]string{"annotation1": "newValue"},
			expectedLabels:      map[string]string{"label1": "newValue"},
		},
		{
			name:                "No changes to annotations and labels",
			existingAnnotations: map[string]string{"annotation1": "value1"},
			existingLabels:      map[string]string{"label1": "value1"},
			newAnnotations:      map[string]string{"annotation1": "value1"},
			newLabels:           map[string]string{"label1": "value1"},
			expectedAnnotations: map[string]string{"annotation1": "value1"},
			expectedLabels:      map[string]string{"label1": "value1"},
		},
		{
			name:                "Add annotations without labels",
			existingAnnotations: nil,
			existingLabels:      map[string]string{"label1": "value1"},
			newAnnotations:      map[string]string{"annotation1": "value1"},
			newLabels:           nil,
			expectedAnnotations: map[string]string{"annotation1": "value1"},
			expectedLabels:      map[string]string{"label1": "value1"},
		},
		{
			name:                "Add labels without annotations",
			existingAnnotations: map[string]string{"annotation1": "value1"},
			existingLabels:      nil,
			newAnnotations:      nil,
			newLabels:           map[string]string{"label1": "value1"},
			expectedAnnotations: map[string]string{"annotation1": "value1"},
			expectedLabels:      map[string]string{"label1": "value1"},
		},
		{
			name:                "Add multiple annotations",
			existingAnnotations: nil,
			existingLabels:      nil,
			newAnnotations: map[string]string{
				"annotation1": "value1", "annotation2": "value2"},
			newLabels: map[string]string{"label1": "value1"},
			expectedAnnotations: map[string]string{
				"annotation1": "value1", "annotation2": "value2"},
			expectedLabels: map[string]string{"label1": "value1"},
		},
		{
			name: "Update multiple existing annotations",
			existingAnnotations: map[string]string{
				"annotation1": "oldValue1", "annotation2": "oldValue2"},
			existingLabels: map[string]string{"label1": "oldValue"},
			newAnnotations: map[string]string{
				"annotation3": "newValue3"},
			newLabels: map[string]string{"label1": "newValue"},
			expectedAnnotations: map[string]string{
				"annotation1": "oldValue1", "annotation2": "oldValue2", "annotation3": "newValue3"},
			expectedLabels: map[string]string{"label1": "newValue"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmName := "test-configmap"
			cm := &corev1.ConfigMap{}
			err := fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      cmName,
				Namespace: "default",
			}, cm)
			if err == nil {
				err = fakeClient.Delete(context.TODO(), cm)
			}
			if err != nil && !apierrors.IsNotFound(err) {
				assert.NoError(t, err)
			}

			resource := &corev1.ConfigMap{}
			resource.SetName(cmName)
			resource.SetNamespace("default")
			resource.SetAnnotations(tt.existingAnnotations)
			resource.SetLabels(tt.existingLabels)

			err = fakeClient.Create(context.TODO(), resource)
			assert.NoError(t, err)

			err = r.AddAnnotationsAndLabel(context.TODO(), resource, tt.newAnnotations, tt.newLabels)

			assert.NoError(t, err)

			updatedResource := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      cmName,
				Namespace: "default",
			}, updatedResource)
			assert.NoError(t, err)

			assert.Equal(t, tt.expectedAnnotations, updatedResource.GetAnnotations())

			assert.Equal(t, tt.expectedLabels, updatedResource.GetLabels())
		})
	}
}

func TestRemoveHRNameFromAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &BaseReconciler{
		Client: fakeClient,
	}

	tests := []struct {
		name                string
		existingAnnotations map[string]string
		hrNameToRemove      string
		expectedAnnotations map[string]string
	}{
		{
			name:                "Remove single HR name from annotations",
			existingAnnotations: map[string]string{HRNameAnnotation: "hr1"},
			hrNameToRemove:      "hr1",
			expectedAnnotations: map[string]string(nil),
		},
		{
			name:                "Remove one HR name from a list",
			existingAnnotations: map[string]string{HRNameAnnotation: "hr1,hr2,hr3"},
			hrNameToRemove:      "hr2",
			expectedAnnotations: map[string]string{HRNameAnnotation: "hr1,hr3"},
		},
		{
			name:                "HR name not found in annotations",
			existingAnnotations: map[string]string{HRNameAnnotation: "hr1,hr2"},
			hrNameToRemove:      "hr3",
			expectedAnnotations: map[string]string{HRNameAnnotation: "hr1,hr2"},
		},
		{
			name:                "No annotations present",
			existingAnnotations: nil,
			hrNameToRemove:      "hr1",
			expectedAnnotations: nil,
		},
		{
			name:                "Remove last HR name from a list",
			existingAnnotations: map[string]string{HRNameAnnotation: "hr1"},
			hrNameToRemove:      "hr1",
			expectedAnnotations: map[string]string(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmName := "test-configmap"
			cm := &corev1.ConfigMap{}
			err := fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      cmName,
				Namespace: "default",
			}, cm)
			if err == nil {
				err = fakeClient.Delete(context.TODO(), cm)
			}
			if err != nil && !apierrors.IsNotFound(err) {
				assert.NoError(t, err)
			}

			resource := &corev1.ConfigMap{}
			resource.SetName(cmName)
			resource.SetNamespace("default")
			resource.SetAnnotations(tt.existingAnnotations)

			err = fakeClient.Create(context.TODO(), resource)
			assert.NoError(t, err)

			err = r.RemoveHRNameFromAnnotations(context.TODO(), resource, tt.hrNameToRemove)

			assert.NoError(t, err)

			updatedResource := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), client.ObjectKey{
				Name:      cmName,
				Namespace: "default",
			}, updatedResource)
			assert.NoError(t, err)

			assert.Equal(t, tt.expectedAnnotations, updatedResource.GetAnnotations())
		})
	}
}
