// +kubebuilder:object:generate=true
// +groupName=platform.internal
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "platform.internal", Version: "v1"}
	GroupVersion       = SchemeGroupVersion

	SchemeBuilder = runtime.NewSchemeBuilder(func(scheme *runtime.Scheme) error {
		metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
)
