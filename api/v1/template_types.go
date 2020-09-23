/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TemplateSpec defines the desired state of Template
type TemplateSpec struct {

	// Source selects objects on which to use as a templating object
	Source ResourceSelector `json:"source,omitempty"`

	// Target optionally allows to lookup related resources to patch, defaults
	// to the source object selected
	// +optional
	PatchTarget ResourceSelector `json:"patchTarget,omitempty"`

	// Resources is a list of new resources to create for each source objected found
	// Must specify at least resources or patches or both
	// +optional
	Resources []json.RawMessage `json:"resources,omitempty"`

	// Patches is list of strategic merge patches to apply to to the targets
	// Must specify at least resources or patches or both
	// +optional
	Patches []string `json:"patches,omitempty"`

	// Onceoff will not apply templating more than once (usually at admission stage)
	Onceoff bool `json:"onceoff,omitempty"`
}

// TemplateStatus defines the observed state of Template
type TemplateStatus struct {
}

type ResourceSelector struct {
	LabelSelector      metav1.LabelSelector `json:"labelSelector,omitempty"`
	NamespaceSelector  metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	AnnotationSelector map[string]string    `json:"annotationSelector,omitempty"`
	FieldSelector      string               `json:"fieldSelector,omitempty"`
}

type ObjectSelector struct {
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// +kubebuilder:object:root=true

// Template is the Schema for the templates API
type Template struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemplateSpec   `json:"spec,omitempty"`
	Status TemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TemplateList contains a list of Template
type TemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Template `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Template{}, &TemplateList{})
}
