// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	v2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
)

type CostCenterResource struct {
	Type *string `json:"type,omitempty"`
	Name *string `json:"name,omitempty"`
}

type CostCenterInitParameters struct {
	Enterprise *string `json:"enterprise"`
	Name       *string `json:"name"`
}

type CostCenterObservation struct {
	ID        *string              `json:"id,omitempty"`
	Name      *string              `json:"name,omitempty"`
	State     *string              `json:"state,omitempty"`
	Resources []CostCenterResource `json:"resources,omitempty"`
}

type CostCenterParameters struct {
	Enterprise *string `json:"enterprise"`
	// +kubebuilder:validation:MaxLength=255
	Name          *string  `json:"name"`
	Organizations []string `json:"organizations,omitempty"`
	Repositories  []string `json:"repositories,omitempty"`
}

type CostCenterSpec struct {
	v2.ManagedResourceSpec `json:",inline"`
	ForProvider            CostCenterParameters `json:"forProvider"`
}

type CostCenterStatus struct {
	v1.ResourceStatus `json:",inline"`
	AtProvider        CostCenterObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
type CostCenter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CostCenterSpec   `json:"spec"`
	Status            CostCenterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CostCenterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CostCenter `json:"items"`
}

var (
	CostCenterKind             = "CostCenter"
	CostCenterGroupKind        = schema.GroupKind{Group: CRDGroup, Kind: CostCenterKind}.String()
	CostCenterKindAPIVersion   = CostCenterKind + "." + CRDGroupVersion.String()
	CostCenterGroupVersionKind = CRDGroupVersion.WithKind(CostCenterKind)
)

func init() {
	SchemeBuilder.Register(&CostCenter{}, &CostCenterList{})
}
