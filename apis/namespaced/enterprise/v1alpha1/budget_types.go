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

type BudgetAlertingParameters struct {
	// +kubebuilder:validation:Required
	WillAlert       *bool    `json:"willAlert"`
	AlertRecipients []string `json:"alertRecipients,omitempty"`
}

type BudgetAlertingObservation struct {
	WillAlert       *bool    `json:"willAlert,omitempty"`
	AlertRecipients []string `json:"alertRecipients,omitempty"`
}

type BudgetInitParameters struct {
	Enterprise *string `json:"enterprise"`
}

type BudgetObservation struct {
	ID             *string                    `json:"id,omitempty"`
	ConsumedAmount *float64                   `json:"consumedAmount,omitempty"`
	BudgetAlerting *BudgetAlertingObservation `json:"budgetAlerting,omitempty"`
}

type BudgetParameters struct {
	Enterprise *string `json:"enterprise"`
	// +kubebuilder:validation:Enum=enterprise;organization;repository;cost_center;multi_user_customer;multi_user_cost_center;user
	BudgetScope      *string `json:"budgetScope"`
	BudgetEntityName *string `json:"budgetEntityName,omitempty"`
	User             *string `json:"user,omitempty"`
	// +kubebuilder:validation:Enum=BundlePricing;ProductPricing;SkuPricing
	BudgetType       *string `json:"budgetType"`
	BudgetProductSKU *string `json:"budgetProductSku,omitempty"`
	// +kubebuilder:validation:Minimum=0
	BudgetAmount        *int64                    `json:"budgetAmount"`
	PreventFurtherUsage *bool                     `json:"preventFurtherUsage"`
	BudgetAlerting      *BudgetAlertingParameters `json:"budgetAlerting"`
	ExpiresAt           *string                   `json:"expiresAt,omitempty"`
}

type BudgetSpec struct {
	v2.ManagedResourceSpec `json:",inline"`
	ForProvider            BudgetParameters `json:"forProvider"`
}

type BudgetStatus struct {
	v1.ResourceStatus `json:",inline"`
	AtProvider        BudgetObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
type Budget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BudgetSpec   `json:"spec"`
	Status            BudgetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Budget `json:"items"`
}

// Budget type metadata.
var (
	BudgetKind             = "Budget"
	BudgetGroupKind        = schema.GroupKind{Group: CRDGroup, Kind: BudgetKind}.String()
	BudgetKindAPIVersion   = BudgetKind + "." + CRDGroupVersion.String()
	BudgetGroupVersionKind = CRDGroupVersion.WithKind(BudgetKind)
)

func init() {
	SchemeBuilder.Register(&Budget{}, &BudgetList{})
}
