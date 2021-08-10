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
	"github.com/flanksource/kommons"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RESTSpec defines the desired state of REST
type RESTSpec struct {
	// URL represents the URL address used to send requests
	URL string `json:"url,omitempty"`

	// Auth may be used for http basic authentication
	// +optional
	Auth *RESTAuth `json:"auth,omitempty"`

	// Headers are optional http headers to be sent on the request
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// Update defines the payload to be sent when CRD item is updated
	Update RESTAction `json:"update,omitempty"`

	// Remove defines the payload to be sent when CRD item is deleted
	Remove RESTAction `json:"remove,omitempty"`
}

type RESTAuth struct {
	// Username represents the HTTP Basic Auth username
	Username kommons.EnvVarSource `json:"username,omitempty"`
	// Password represents the HTTP Basic Auth password
	Password kommons.EnvVarSource `json:"password,omitempty"`
	// Namespace where secret / config map is present
	Namespace string `json:"namespace,omitempty"`
}

type RESTAction struct {
	// Method represents HTTP method to be used for the request. Example: POST
	Method string `json:"method,omitempty"`
	// URL represents the URL used for the request
	// +optional
	URL string `json:"url,omitempty"`
	// Body represents the HTTP Request body
	// +optional
	Body string `json:"body,omitempty"`
	// Status defines the status fields which will be updated based on response status
	// +optional
	Status map[string]string `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope="Cluster"
// +kubebuilder:subresource:status
// REST is the Schema for the rest API
type REST struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RESTSpec `json:"spec"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Status map[string]string `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TemplateList contains a list of Template
type RESTList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []REST `json:"items"`
}

func init() {
	SchemeBuilder.Register(&REST{}, &RESTList{})
}
