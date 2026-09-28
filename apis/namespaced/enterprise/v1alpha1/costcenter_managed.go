/*
Copyright 2022 Upbound Inc.
*/

package v1alpha1

import xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"

func (mg *CostCenter) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return mg.Status.GetCondition(ct)
}

func (mg *CostCenter) GetManagementPolicies() xpv1.ManagementPolicies {
	return mg.Spec.ManagementPolicies
}

func (mg *CostCenter) GetProviderConfigReference() *xpv1.ProviderConfigReference {
	return mg.Spec.ProviderConfigReference
}

func (mg *CostCenter) GetWriteConnectionSecretToReference() *xpv1.LocalSecretReference {
	return mg.Spec.WriteConnectionSecretToReference
}

func (mg *CostCenter) SetConditions(c ...xpv1.Condition) {
	mg.Status.SetConditions(c...)
}

func (mg *CostCenter) SetManagementPolicies(r xpv1.ManagementPolicies) {
	mg.Spec.ManagementPolicies = r
}

func (mg *CostCenter) SetProviderConfigReference(r *xpv1.ProviderConfigReference) {
	mg.Spec.ProviderConfigReference = r
}

func (mg *CostCenter) SetWriteConnectionSecretToReference(r *xpv1.LocalSecretReference) {
	mg.Spec.WriteConnectionSecretToReference = r
}
