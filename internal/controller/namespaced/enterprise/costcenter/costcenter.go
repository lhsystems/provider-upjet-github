package costcenter

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
	clustercc "github.com/crossplane-contrib/provider-upjet-github/internal/controller/cluster/enterprise/costcenter"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/upjet/v2/pkg/controller"
)

const finalizer = "finalizer.managedresource.crossplane.io"

func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.CostCenterGroupVersionKind.String())
		}
	}, v1alpha1.CostCenterGroupVersionKind)
	return nil
}

func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := "costcenter-direct-namespaced"
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
		For(&v1alpha1.CostCenter{}).
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
	var cr v1alpha1.CostCenter
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

func (r *reconciler) reconcileExternal(ctx context.Context, cr *v1alpha1.CostCenter, service clustercc.GitHubService) (ctrl.Result, error) {
	centers, err := service.ListCostCenters(ctx, *cr.Spec.ForProvider.Enterprise)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	found := findCostCenter(centers, cr)
	found, err = reconcileCostCenterState(ctx, service, cr, found)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	if found == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
	}
	cr.Status.AtProvider.ID, cr.Status.AtProvider.Name, cr.Status.AtProvider.State = found.ID, found.Name, found.State
	cr.Status.AtProvider.Resources = make([]v1alpha1.CostCenterResource, len(found.Resources))
	for i, resource := range found.Resources {
		cr.Status.AtProvider.Resources[i] = v1alpha1.CostCenterResource{Type: resource.Type, Name: resource.Name}
	}
	cr.Status.SetConditions(xpv1.ReconcileSuccess(), xpv1.Available())
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}
	if found.ID != nil {
		var latest v1alpha1.CostCenter
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

func reconcileCostCenterState(ctx context.Context, service clustercc.GitHubService, cr *v1alpha1.CostCenter, found *clustercc.CostCenter) (*clustercc.CostCenter, error) {
	allowUpdate := allows(cr, xpv1.ManagementActionUpdate)
	found, err := ensureCostCenter(ctx, service, cr, found, allows(cr, xpv1.ManagementActionCreate), allowUpdate)
	if err != nil || found == nil {
		return found, err
	}
	if allowUpdate {
		if err := syncResources(ctx, service, cr, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func ensureCostCenter(ctx context.Context, service clustercc.GitHubService, cr *v1alpha1.CostCenter, found *clustercc.CostCenter, allowCreate, allowUpdate bool) (*clustercc.CostCenter, error) {
	if found == nil {
		if !allowCreate {
			return nil, nil
		}
		return service.CreateCostCenter(ctx, *cr.Spec.ForProvider.Enterprise, *cr.Spec.ForProvider.Name)
	}
	if allowUpdate && cr.Spec.ForProvider.Name != nil && found.Name != nil && *cr.Spec.ForProvider.Name != *found.Name {
		return service.UpdateCostCenter(ctx, *cr.Spec.ForProvider.Enterprise, *found.ID, *cr.Spec.ForProvider.Name)
	}
	return found, nil
}

func findCostCenter(centers []clustercc.CostCenter, cr *v1alpha1.CostCenter) *clustercc.CostCenter {
	for i := range centers {
		if cr.Status.AtProvider.ID != nil && centers[i].ID != nil && *cr.Status.AtProvider.ID == *centers[i].ID {
			return &centers[i]
		}
	}
	for i := range centers {
		if cr.Spec.ForProvider.Name != nil && centers[i].Name != nil && *cr.Spec.ForProvider.Name == *centers[i].Name {
			return &centers[i]
		}
	}
	return nil
}

func syncResources(ctx context.Context, service clustercc.GitHubService, cr *v1alpha1.CostCenter, found *clustercc.CostCenter) error {
	if found.ID == nil || cr.Spec.ForProvider.Enterprise == nil {
		return errors.New("cost center ID and enterprise are required")
	}
	currentOrganizations := resourceNames(found.Resources, "Org")
	currentRepositories := resourceNames(found.Resources, "Repo")
	if err := syncResourceSet(ctx, service, *cr.Spec.ForProvider.Enterprise, *found.ID, "Organization", cr.Spec.ForProvider.Organizations, currentOrganizations); err != nil {
		return err
	}
	return syncResourceSet(ctx, service, *cr.Spec.ForProvider.Enterprise, *found.ID, "Repo", cr.Spec.ForProvider.Repositories, currentRepositories)
}

func syncResourceSet(ctx context.Context, service clustercc.GitHubService, enterprise, id, resourceType string, desired, current []string) error {
	if added := difference(desired, current); len(added) > 0 {
		if err := service.AddResourcesToCostCenter(ctx, enterprise, id, resourceType, added); err != nil {
			return err
		}
	}
	if removed := difference(current, desired); len(removed) > 0 {
		if err := service.RemoveResourcesFromCostCenter(ctx, enterprise, id, resourceType, removed); err != nil {
			return err
		}
	}
	return nil
}

func resourceNames(resources []clustercc.Resource, resourceType string) []string {
	var names []string
	for _, resource := range resources {
		if resource.Type != nil && *resource.Type == resourceType && resource.Name != nil {
			names = append(names, *resource.Name)
		}
	}
	return names
}

func difference(left, right []string) []string {
	known := make(map[string]struct{}, len(right))
	for _, value := range right {
		known[value] = struct{}{}
	}
	var result []string
	for _, value := range left {
		if _, exists := known[value]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func (r *reconciler) service(ctx context.Context, cr *v1alpha1.CostCenter) (clustercc.GitHubService, error) {
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

func (r *reconciler) providerConfig(ctx context.Context, cr *v1alpha1.CostCenter) (*apisv1beta1.ProviderConfig, error) {
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

func (r *reconciler) delete(ctx context.Context, cr *v1alpha1.CostCenter) (ctrl.Result, error) {
	if !contains(cr.GetFinalizers(), finalizer) {
		return ctrl.Result{}, nil
	}
	if !allows(cr, xpv1.ManagementActionDelete) {
		cr.SetFinalizers(remove(cr.GetFinalizers(), finalizer))
		return ctrl.Result{}, r.Update(ctx, cr)
	}
	costCenterID := cr.Status.AtProvider.ID
	if costCenterID == nil {
		if externalName := meta.GetExternalName(cr); externalName != "" {
			costCenterID = &externalName
		}
	}
	if costCenterID != nil {
		if err := r.deleteExternalCostCenter(ctx, cr, *costCenterID); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
	}
	cr.SetFinalizers(remove(cr.GetFinalizers(), finalizer))
	return ctrl.Result{}, r.Update(ctx, cr)
}

func (r *reconciler) deleteExternalCostCenter(ctx context.Context, cr *v1alpha1.CostCenter, costCenterID string) error {
	service, err := r.service(ctx, cr)
	if err != nil {
		return err
	}
	if err := service.DeleteCostCenter(ctx, *cr.Spec.ForProvider.Enterprise, costCenterID); err != nil {
		return ignoreNotFound(err)
	}

	costCenter, err := service.GetCostCenter(ctx, *cr.Spec.ForProvider.Enterprise, costCenterID)
	if err != nil {
		return ignoreNotFound(err)
	}
	if costCenter.State == nil || *costCenter.State != "deleted" {
		return errors.New("cost center deletion is still in progress")
	}
	return nil
}

func ignoreNotFound(err error) error {
	var notFoundErr *clustercc.NotFoundError
	if errors.As(err, &notFoundErr) {
		return nil
	}
	return err
}

func allows(cr *v1alpha1.CostCenter, action xpv1.ManagementAction) bool {
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
