// SPDX-FileCopyrightText: 2024 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package budget

import (
	"context"
	"encoding/json"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/enterprise/v1alpha1"
	apisv1beta1 "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/v1beta1"
	clustercc "github.com/crossplane-contrib/provider-upjet-github/internal/controller/cluster/enterprise/budget"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/upjet/v2/pkg/controller"
)

const finalizer = "finalizer.managedresource.crossplane.io"

// SetupGated adds a controller for Budget that is registered once its CRD has been established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.BudgetGroupVersionKind.String())
		}
	}, v1alpha1.BudgetGroupVersionKind)
	return nil
}

// Setup adds a controller that reconciles namespaced Budget managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := "budget-direct-namespaced"
	r := &reconciler{
		Client:       mgr.GetClient(),
		Logger:       o.Logger.WithValues("controller", name),
		recorder:     event.NewAPIRecorder(mgr.GetEventRecorderFor(name)),
		newService:   clustercc.NewGitHubService,
		usageTracker: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1beta1.ProviderConfigUsage{}),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.Budget{}).
		Complete(r)
}

type reconciler struct {
	client.Client
	Logger       logging.Logger
	recorder     event.Recorder
	newService   func(context.Context, clustercc.GithubCredentials) (clustercc.GitHubService, error)
	usageTracker *resource.ProviderConfigUsageTracker
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cr v1alpha1.Budget
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if cr.GetDeletionTimestamp() != nil {
		return r.delete(ctx, &cr)
	}
	if !contains(cr.GetFinalizers(), finalizer) {
		cr.SetFinalizers(append(cr.GetFinalizers(), finalizer))
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err := r.usageTracker.Track(ctx, &cr); err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, errors.Wrap(err, "cannot track ProviderConfig usage")
	}

	service, err := r.service(ctx, &cr)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	return r.reconcileExternal(ctx, &cr, service)
}

func (r *reconciler) reconcileExternal(ctx context.Context, cr *v1alpha1.Budget, service clustercc.GitHubService) (ctrl.Result, error) {
	budgets, err := service.ListBudgets(ctx, *cr.Spec.ForProvider.Enterprise)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	found := findBudget(budgets, cr)
	found, err = reconcileBudgetState(ctx, service, cr, found)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	if found == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
	}

	cr.Status.AtProvider.ID = found.ID
	cr.Status.AtProvider.ConsumedAmount = found.ConsumedAmount
	if found.BudgetAlerting != nil {
		cr.Status.AtProvider.BudgetAlerting = &v1alpha1.BudgetAlertingObservation{
			WillAlert:       found.BudgetAlerting.WillAlert,
			AlertRecipients: found.BudgetAlerting.AlertRecipients,
		}
	}
	cr.Status.SetConditions(xpv1.ReconcileSuccess(), xpv1.Available())
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	if found.ID != nil {
		var latest v1alpha1.Budget
		if err := r.Get(ctx, client.ObjectKeyFromObject(cr), &latest); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		annotations := latest.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["crossplane.io/external-name"] = *found.ID
		latest.SetAnnotations(annotations)
		if err := r.Update(ctx, &latest); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
	}
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func reconcileBudgetState(ctx context.Context, service clustercc.GitHubService, cr *v1alpha1.Budget, found *clustercc.Budget) (*clustercc.Budget, error) {
	if found == nil {
		if !allows(cr, xpv1.ManagementActionCreate) {
			return nil, nil
		}
		created, err := service.CreateBudget(ctx, *cr.Spec.ForProvider.Enterprise, toExternalBudget(cr.Spec.ForProvider))
		if err != nil {
			return nil, err
		}
		return created, nil
	}
	if allows(cr, xpv1.ManagementActionUpdate) && !isUpToDate(found, cr.Spec.ForProvider) {
		updated, err := service.UpdateBudget(ctx, *cr.Spec.ForProvider.Enterprise, *found.ID, toExternalBudget(cr.Spec.ForProvider))
		if err != nil {
			return nil, err
		}
		return updated, nil
	}
	return found, nil
}

func findBudget(budgets []clustercc.Budget, cr *v1alpha1.Budget) *clustercc.Budget {
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
func matches(budget *clustercc.Budget, spec v1alpha1.BudgetParameters) bool {
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

// isUpToDate compares only the mutable fields of a budget (amount, usage
// prevention, alerting, and expiry), since scope/entity/type/sku/user are
// immutable identity fields enforced via matches().
func isUpToDate(budget *clustercc.Budget, spec v1alpha1.BudgetParameters) bool {
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
func alertingUpToDate(remote *clustercc.BudgetAlerting, desired *v1alpha1.BudgetAlertingParameters) bool {
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

func toExternalBudget(spec v1alpha1.BudgetParameters) *clustercc.Budget {
	budget := &clustercc.Budget{
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
		budget.BudgetAlerting = &clustercc.BudgetAlerting{
			WillAlert:       spec.BudgetAlerting.WillAlert,
			AlertRecipients: spec.BudgetAlerting.AlertRecipients,
		}
	}
	return budget
}

func (r *reconciler) service(ctx context.Context, cr *v1alpha1.Budget) (clustercc.GitHubService, error) {
	pc, err := r.providerConfig(ctx, cr)
	if err != nil {
		return nil, err
	}
	credentials := pc.Spec.Credentials
	if credentials.SecretRef != nil {
		secretRef := *credentials.SecretRef
		secretRef.Namespace = cr.Namespace
		credentials.SecretRef = &secretRef
	}
	data, err := resource.CommonCredentialExtractor(ctx, credentials.Source, r.Client, credentials.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get credentials")
	}
	var creds clustercc.GithubCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, errors.Wrap(err, "failed to parse GitHub credentials JSON")
	}
	return r.newService(ctx, creds)
}

func (r *reconciler) providerConfig(ctx context.Context, cr *v1alpha1.Budget) (*apisv1beta1.ProviderConfig, error) {
	ref := cr.GetProviderConfigReference()
	if ref == nil || ref.Name == "" {
		return nil, errors.New("ProviderConfig reference is required")
	}
	pc := &apisv1beta1.ProviderConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: cr.Namespace}, pc); err != nil {
		return nil, errors.Wrap(err, "cannot get ProviderConfig")
	}
	return pc, nil
}

func (r *reconciler) delete(ctx context.Context, cr *v1alpha1.Budget) (ctrl.Result, error) {
	if !contains(cr.GetFinalizers(), finalizer) {
		return ctrl.Result{}, nil
	}
	if !allows(cr, xpv1.ManagementActionDelete) {
		cr.SetFinalizers(remove(cr.GetFinalizers(), finalizer))
		return ctrl.Result{}, r.Update(ctx, cr)
	}
	budgetID := cr.Status.AtProvider.ID
	if budgetID == nil {
		if externalName := meta.GetExternalName(cr); externalName != "" {
			budgetID = &externalName
		}
	}
	if budgetID != nil {
		if err := r.deleteExternalBudget(ctx, cr, *budgetID); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
	}
	cr.SetFinalizers(remove(cr.GetFinalizers(), finalizer))
	return ctrl.Result{}, r.Update(ctx, cr)
}

func (r *reconciler) deleteExternalBudget(ctx context.Context, cr *v1alpha1.Budget, budgetID string) error {
	service, err := r.service(ctx, cr)
	if err != nil {
		return err
	}
	// Unlike Cost Centers, GitHub Budgets do not have a "state" field and are
	// deleted synchronously, so no post-delete verification is required.
	return ignoreNotFound(service.DeleteBudget(ctx, *cr.Spec.ForProvider.Enterprise, budgetID))
}

func ignoreNotFound(err error) error {
	var notFoundErr *clustercc.NotFoundError
	if errors.As(err, &notFoundErr) {
		return nil
	}
	return err
}

func allows(cr *v1alpha1.Budget, action xpv1.ManagementAction) bool {
	policies := cr.GetManagementPolicies()
	if len(policies) == 0 {
		return true
	}
	for _, policy := range policies {
		if policy == xpv1.ManagementActionAll || policy == action {
			return true
		}
	}
	return false
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func remove(values []string, value string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}
