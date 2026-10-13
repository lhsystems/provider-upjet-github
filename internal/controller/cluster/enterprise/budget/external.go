/*
Copyright 2022 Upbound Inc.
*/

package budget

import (
	"context"
	"encoding/json"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/crossplane-contrib/provider-upjet-github/apis/cluster/enterprise/v1alpha1"
	apisv1beta1 "github.com/crossplane-contrib/provider-upjet-github/apis/cluster/v1beta1"
)

// ExternalObservation reports the state of the external Budget resource.
type ExternalObservation struct {
	ResourceExists          bool
	ResourceUpToDate        bool
	ResourceLateInitialized bool
	ConnectionDetails       map[string][]byte
}

// ExternalCreation reports the result of creating the external Budget resource.
type ExternalCreation struct {
	ConnectionDetails map[string][]byte
}

// ExternalUpdate reports the result of updating the external Budget resource.
type ExternalUpdate struct{}

type external struct {
	service GitHubService
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Budget)
	if !ok {
		return ExternalObservation{}, errors.New(errNotBudget)
	}

	budgets, err := e.service.ListBudgets(ctx, *cr.Spec.ForProvider.Enterprise)
	if err != nil {
		return ExternalObservation{}, errors.Wrap(err, "failed to list budgets")
	}

	target := findTargetBudget(budgets, cr)
	if target == nil {
		return ExternalObservation{ResourceExists: false}, nil
	}

	updateStatus(cr, target)

	return ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: isUpToDate(target, cr.Spec.ForProvider),
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Budget)
	if !ok {
		return ExternalCreation{}, errors.New(errNotBudget)
	}

	desired := toExternalBudget(cr.Spec.ForProvider)
	budget, err := e.service.CreateBudget(ctx, *cr.Spec.ForProvider.Enterprise, desired)
	if err != nil {
		return ExternalCreation{}, err
	}

	updateStatus(cr, budget)

	creation := ExternalCreation{}
	if budget.ID != nil {
		creation.ConnectionDetails = map[string][]byte{"id": []byte(*budget.ID)}
	}
	return creation, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Budget)
	if !ok {
		return ExternalUpdate{}, errors.New(errNotBudget)
	}

	enterprise := cr.Spec.ForProvider.Enterprise
	id := cr.Status.AtProvider.ID
	if enterprise == nil || id == nil {
		return ExternalUpdate{}, errors.New("enterprise and id must be specified for update")
	}

	desired := toExternalBudget(cr.Spec.ForProvider)
	budget, err := e.service.UpdateBudget(ctx, *enterprise, *id, desired)
	if err != nil {
		return ExternalUpdate{}, err
	}

	updateStatus(cr, budget)

	return ExternalUpdate{}, nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Budget)
	if !ok {
		return errors.New(errNotBudget)
	}

	enterprise := cr.Spec.ForProvider.Enterprise
	id := cr.Status.AtProvider.ID
	if enterprise == nil || id == nil {
		return errors.New("enterprise and id must be specified for deletion")
	}

	// Unlike Cost Centers, GitHub Budgets do not have a "state" field and are
	// deleted synchronously, so no post-delete verification is required.
	if err := e.service.DeleteBudget(ctx, *enterprise, *id); err != nil {
		var notFoundErr *NotFoundError
		if errors.As(err, &notFoundErr) {
			return nil
		}
		return err
	}

	return nil
}

// findTargetBudget locates the remote budget that corresponds to the managed
// resource, first by external ID, then by its scope/entity/sku/user identity.
func findTargetBudget(budgets []Budget, cr *v1alpha1.Budget) *Budget {
	if cr.Status.AtProvider.ID != nil {
		for i := range budgets {
			if budgets[i].ID != nil && *budgets[i].ID == *cr.Status.AtProvider.ID {
				return &budgets[i]
			}
		}
	}
	for i := range budgets {
		if matches(&budgets[i], cr.Spec.ForProvider) {
			return &budgets[i]
		}
	}
	return nil
}

// matches determines whether a remote budget corresponds to the identity
// (scope, entity, SKU, and user) of the desired budget.
func matches(budget *Budget, spec v1alpha1.BudgetParameters) bool {
	if budget.BudgetScope == nil || spec.BudgetScope == nil || *budget.BudgetScope != *spec.BudgetScope {
		return false
	}
	if stringValue(budget.BudgetEntityName) != stringValue(spec.BudgetEntityName) {
		return false
	}
	if stringValue(budget.BudgetProductSKU) != stringValue(spec.BudgetProductSKU) {
		return false
	}
	if stringValue(budget.User) != stringValue(spec.User) {
		return false
	}
	return true
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		if seen[v] == 0 {
			return false
		}
		seen[v]--
	}
	return true
}

func toExternalBudget(spec v1alpha1.BudgetParameters) *Budget {
	budget := &Budget{
		BudgetScope:         spec.BudgetScope,
		BudgetEntityName:    spec.BudgetEntityName,
		User:                spec.User,
		BudgetType:          spec.BudgetType,
		BudgetProductSKU:    spec.BudgetProductSKU,
		BudgetAmount:        spec.BudgetAmount,
		PreventFurtherUsage: spec.PreventFurtherUsage,
		ExpiresAt:           spec.ExpiresAt,
	}
	if spec.BudgetAlerting != nil {
		budget.BudgetAlerting = &BudgetAlerting{
			WillAlert:       spec.BudgetAlerting.WillAlert,
			AlertRecipients: spec.BudgetAlerting.AlertRecipients,
		}
	}
	return budget
}

func updateStatus(cr *v1alpha1.Budget, budget *Budget) {
	cr.Status.AtProvider.ID = budget.ID
	cr.Status.AtProvider.ConsumedAmount = budget.ConsumedAmount
	if budget.BudgetAlerting != nil {
		cr.Status.AtProvider.BudgetAlerting = &v1alpha1.BudgetAlertingObservation{
			WillAlert:       budget.BudgetAlerting.WillAlert,
			AlertRecipients: budget.BudgetAlerting.AlertRecipients,
		}
	}

	if budget.ID != nil {
		meta.SetExternalName(cr, *budget.ID)
	}
}

// isUpToDate compares only the mutable fields of a budget (amount, usage
// prevention, alerting, and expiry), since scope/entity/type/sku/user are
// immutable identity fields enforced via matches().
func isUpToDate(budget *Budget, spec v1alpha1.BudgetParameters) bool {
	if spec.BudgetAmount != nil && (budget.BudgetAmount == nil || *spec.BudgetAmount != *budget.BudgetAmount) {
		return false
	}
	if spec.PreventFurtherUsage != nil && (budget.PreventFurtherUsage == nil || *spec.PreventFurtherUsage != *budget.PreventFurtherUsage) {
		return false
	}
	if !alertingUpToDate(budget.BudgetAlerting, spec.BudgetAlerting) {
		return false
	}
	return stringValue(spec.ExpiresAt) == stringValue(budget.ExpiresAt)
}

// alertingUpToDate compares the desired alerting configuration against the
// remote budget's alerting configuration.
func alertingUpToDate(remote *BudgetAlerting, desired *v1alpha1.BudgetAlertingParameters) bool {
	if desired == nil {
		return true
	}
	if remote == nil {
		return false
	}
	if desired.WillAlert != nil && (remote.WillAlert == nil || *desired.WillAlert != *remote.WillAlert) {
		return false
	}
	return stringSliceEqual(desired.AlertRecipients, remote.AlertRecipients)
}

func (r *DirectBudgetReconciler) getExternalClient(ctx context.Context, cr *v1alpha1.Budget) (*external, error) {
	pc := &apisv1beta1.ProviderConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	cd := pc.Spec.Credentials
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, r.Client, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	var creds GithubCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, errors.Wrap(err, "failed to parse GitHub credentials JSON")
	}

	svc, err := r.newServiceFn(ctx, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}
	return &external{service: svc}, nil
}
