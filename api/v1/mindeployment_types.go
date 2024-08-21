/*
Copyright 2024.

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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MinDeploymentSpec defines the desired state of MinDeployment
type MinDeploymentSpec struct {
	// Minimum number of replicas to for the deployment
	// +kubebuilder:validation:Minimum=1
	Replicas int `json:"replicas,omitempty"`

	// Maximum number of replicas to for the deployment (optional)
	// +kubebuilder:validation:Optional
	MaxReplicas *int `json:"maxReplicas,omitempty"`

	// Template for the pods to start
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Optional
	Template corev1.PodTemplateSpec `json:"template,omitempty"`

	// Name of the source deployment to copy the template from
	// +kubebuilder:validation:Optional
	SourceDeploymentName string `json:"sourceDeploymentName,omitempty"`
}

// MinDeploymentStatus defines the observed state of MinDeployment
type MinDeploymentStatus struct {
	// Current number of replicas
	Replicas       int `json:"replicas,omitempty"`
	NbPodsCreated  int `json:"nbPodsCreated,omitempty"`
	NbPodsDeleted  int `json:"nbPodsDeleted,omitempty"`
	NbPodsOutdated int `json:"nbPodsOutdated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// MinDeployment is the Schema for the mindeployments API
type MinDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MinDeploymentSpec   `json:"spec,omitempty"`
	Status MinDeploymentStatus `json:"status,omitempty"`
}

var ErrInvalidMinMaxReplicas = errors.New("Invalid min/max replicas")

var ErrInvalidTemplate = errors.New("Invalid template")

// Check validates the MinDeployment.
func (m *MinDeployment) Check() error {
	if m.Spec.Replicas < 1 {
		return fmt.Errorf("%w: replicas must be at least 1", ErrInvalidMinMaxReplicas)
	} else if m.Spec.MaxReplicas != nil && *m.Spec.MaxReplicas < m.Spec.Replicas {
		return fmt.Errorf("%w: max replicas must be greater than or equal to replicas", ErrInvalidMinMaxReplicas)
	} else if m.Spec.Template.Spec.Containers == nil {
		return errors.New("%w: template must have at least one container")
	}

	return nil
}

// +kubebuilder:object:root=true

// MinDeploymentList contains a list of MinDeployment
type MinDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MinDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MinDeployment{}, &MinDeploymentList{})
}
