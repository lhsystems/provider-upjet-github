/*
Copyright 2022 Upbound Inc.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

// BudgetAlertingParameters defines the desired alerting configuration of a Budget.
type BudgetAlertingParameters struct {
	// Whether alerts are enabled for this budget. Rejected for user-scoped
	// budgets as alerting is always disabled for them.
	// +kubebuilder:validation:Required
	WillAlert *bool `json:"willAlert"`

	// Login names of the users who will receive alerts.
	AlertRecipients []string `json:"alertRecipients,omitempty"`
}

// BudgetAlertingObservation defines the observed alerting configuration of a Budget.
type BudgetAlertingObservation struct {
	// Whether alerts are enabled for this budget.
	WillAlert *bool `json:"willAlert,omitempty"`

	// Login names of the users who will receive alerts.
	AlertRecipients []string `json:"alertRecipients,omitempty"`
}

// BudgetInitParameters defines the desired state for initialization
type BudgetInitParameters struct {
	// The name of the enterprise
	// +kubebuilder:validation:Required
	Enterprise *string `json:"enterprise"`
}

// BudgetObservation defines the observed state of Budget
type BudgetObservation struct {
	// The unique identifier of the budget
	ID *string `json:"id,omitempty"`

	// The amount consumed against this budget so far
	ConsumedAmount *float64 `json:"consumedAmount,omitempty"`

	// The alerting configuration reported by GitHub for this budget
	BudgetAlerting *BudgetAlertingObservation `json:"budgetAlerting,omitempty"`
}

// BudgetParameters defines the desired state parameters
type BudgetParameters struct {
	// The name of the enterprise
	// +kubebuilder:validation:Required
	Enterprise *string `json:"enterprise"`

	// The scope the budget applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=enterprise;organization;repository;cost_center;multi_user_customer;multi_user_cost_center;user
	BudgetScope *string `json:"budgetScope"`

	// The name of the entity (organization, repository, or cost center) the
	// budget applies to. Leave empty for enterprise-scoped and
	// multi_user_customer-scoped budgets.
	BudgetEntityName *string `json:"budgetEntityName,omitempty"`

	// The GitHub username the budget applies to. Required when budgetScope is user.
	User *string `json:"user,omitempty"`

	// The pricing model used by the budget. Determines how budgetProductSku is interpreted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=BundlePricing;ProductPricing;SkuPricing
	BudgetType *string `json:"budgetType"`

	// A single product or SKU that will be covered by the budget.
	BudgetProductSKU *string `json:"budgetProductSku,omitempty"`

	// The budget amount in whole dollars. For license-based products, this
	// represents the number of licenses.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=0
	BudgetAmount *int64 `json:"budgetAmount"`

	// Whether to prevent additional spending once the budget is exceeded.
	// For user and multi_user_customer scopes, this must be true.
	// +kubebuilder:validation:Required
	PreventFurtherUsage *bool `json:"preventFurtherUsage"`

	// Alerting configuration for the budget.
	// +kubebuilder:validation:Required
	BudgetAlerting *BudgetAlertingParameters `json:"budgetAlerting"`

	// The date the budget will expire, in YYYY-MM-DD format. Only supported
	// for budgets with a user scope. If not provided, the budget will not expire.
	ExpiresAt *string `json:"expiresAt,omitempty"`
}

// BudgetSpec defines the desired state of Budget
type BudgetSpec struct {
	v1.ResourceSpec `json:",inline"`
	ForProvider     BudgetParameters `json:"forProvider"`
}

// BudgetStatus defines the observed state of Budget
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
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}

// Budget is the Schema for the Budgets API. Manages a GitHub Enterprise Budget.
type Budget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BudgetSpec   `json:"spec"`
	Status BudgetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BudgetList contains a list of Budgets
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
