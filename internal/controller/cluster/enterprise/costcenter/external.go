/*
Copyright 2022 Upbound Inc.
*/

package costcenter

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

// ExternalObservation represents the result of observing an external resource
type ExternalObservation struct {
	ResourceExists          bool
	ResourceUpToDate        bool
	ResourceLateInitialized bool
	ConnectionDetails       map[string][]byte
}

// ExternalCreation represents the result of creating an external resource
type ExternalCreation struct {
	ConnectionDetails map[string][]byte
}

// ExternalUpdate represents the result of updating an external resource
type ExternalUpdate struct{}

// external implements the external client interface
type external struct {
	service GitHubService
}

// syncCostCenterResources ensures organizations and repositories are mapped to the cost center
func (e *external) syncCostCenterResources(ctx context.Context, cc *CostCenter, cr *v1alpha1.CostCenter) error {
	costCenterID := cc.ID
	if costCenterID == nil {
		return errors.New("cost center ID is nil")
	}
	enterprise := cr.Spec.ForProvider.Enterprise
	if enterprise == nil {
		return errors.New("enterprise is nil")
	}

	type resourceSync struct {
		resourceType string
		desired      []string
		current      []string
	}
	syncs := []resourceSync{
		{"Organization", cr.Spec.ForProvider.Organizations, getResourceNamesByType(cc.Resources, "Org")},
		{"Repo", cr.Spec.ForProvider.Repositories, getResourceNamesByType(cc.Resources, "Repo")},
	}
	for _, s := range syncs {
		if err := e.applySyncDiff(ctx, *enterprise, *costCenterID, s.resourceType, s.desired, s.current); err != nil {
			return err
		}
	}
	return nil
}

func (e *external) applySyncDiff(ctx context.Context, enterprise, costCenterID, resourceType string, desired, current []string) error {
	if toAdd := diff(desired, current); len(toAdd) > 0 {
		if err := e.service.AddResourcesToCostCenter(ctx, enterprise, costCenterID, resourceType, toAdd); err != nil {
			return err
		}
	}
	if toRemove := diff(current, desired); len(toRemove) > 0 {
		if err := e.service.RemoveResourcesFromCostCenter(ctx, enterprise, costCenterID, resourceType, toRemove); err != nil {
			return err
		}
	}
	return nil
}

// getResourceNamesByType returns the names of resources of a given type
func getResourceNamesByType(resources []Resource, typ string) []string {
	var names []string
	for _, r := range resources {
		if r.Type != nil && *r.Type == typ && r.Name != nil {
			names = append(names, *r.Name)
		}
	}
	return names
}

// diff returns items in a but not in b
func diff(a, b []string) []string {
	m := make(map[string]struct{}, len(b))
	for _, x := range b {
		m[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, found := m[x]; !found {
			out = append(out, x)
		}
	}
	return out
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.CostCenter)
	if !ok {
		return ExternalObservation{}, errors.New("managed resource is not a CostCenter")
	}

	costCenters, err := e.service.ListCostCenters(ctx, *cr.Spec.ForProvider.Enterprise)
	if err != nil {
		return ExternalObservation{}, errors.Wrap(err, "failed to list cost centers")
	}

	targetCostCenter := e.findTargetCostCenter(costCenters, cr)

	if targetCostCenter == nil {
		return ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	if !e.isResourceActive(targetCostCenter) {
		return ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	e.updateStatus(cr, targetCostCenter)

	err = e.syncCostCenterResources(ctx, targetCostCenter, cr)
	if err != nil {
		return ExternalObservation{}, errors.Wrap(err, "failed to sync cost center resources")
	}

	return ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: e.isUpToDate(targetCostCenter, cr.Spec.ForProvider),
	}, nil
}

// findTargetCostCenter searches for the cost center by ID first, then by name
func (e *external) findTargetCostCenter(costCenters []CostCenter, cr *v1alpha1.CostCenter) *CostCenter {
	var targetCostCenter *CostCenter

	if cr.Status.AtProvider.ID != nil {
		targetCostCenter = e.findCostCenterByID(costCenters, *cr.Status.AtProvider.ID)
	}

	if targetCostCenter == nil {
		targetCostCenter = e.findCostCenterByName(costCenters, *cr.Spec.ForProvider.Name)
	}

	return targetCostCenter
}

// findCostCenterByID searches for a cost center by its ID
func (e *external) findCostCenterByID(costCenters []CostCenter, id string) *CostCenter {
	for i := range costCenters {
		cc := &costCenters[i]
		if cc.ID != nil && *cc.ID == id {
			return cc
		}
	}
	return nil
}

// findCostCenterByName searches for a cost center by its name
func (e *external) findCostCenterByName(costCenters []CostCenter, name string) *CostCenter {
	for i := range costCenters {
		cc := &costCenters[i]
		if cc.Name != nil && *cc.Name == name {
			return cc
		}
	}
	return nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.CostCenter)
	if !ok {
		return ExternalCreation{}, errors.New(errNotCostCenter)
	}

	enterprise := cr.Spec.ForProvider.Enterprise
	name := cr.Spec.ForProvider.Name
	if enterprise == nil || name == nil {
		return ExternalCreation{}, errors.New("enterprise and name must be specified")
	}

	costCenter, err := e.service.CreateCostCenter(ctx, *enterprise, *name)
	if err != nil {
		return ExternalCreation{}, err
	}

	e.updateStatus(cr, costCenter)

	return ExternalCreation{
		ConnectionDetails: map[string][]byte{
			"id":   []byte(*costCenter.ID),
			"name": []byte(*costCenter.Name),
		},
	}, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.CostCenter)
	if !ok {
		return ExternalUpdate{}, errors.New(errNotCostCenter)
	}

	enterprise := cr.Spec.ForProvider.Enterprise
	name := cr.Spec.ForProvider.Name
	id := cr.Status.AtProvider.ID

	if enterprise == nil || name == nil || id == nil {
		return ExternalUpdate{}, errors.New("enterprise, name, and id must be specified for update")
	}

	costCenter, err := e.service.UpdateCostCenter(ctx, *enterprise, *id, *name)
	if err != nil {
		return ExternalUpdate{}, err
	}

	e.updateStatus(cr, costCenter)

	return ExternalUpdate{}, nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.CostCenter)
	if !ok {
		return errors.New(errNotCostCenter)
	}

	enterprise := cr.Spec.ForProvider.Enterprise
	id := cr.Status.AtProvider.ID

	if enterprise == nil || id == nil {
		return errors.New("enterprise and id must be specified for deletion")
	}

	return e.service.DeleteCostCenter(ctx, *enterprise, *id)
}

// updateStatus updates the status fields and sets the external name annotation
func (e *external) updateStatus(cr *v1alpha1.CostCenter, costCenter *CostCenter) {
	cr.Status.AtProvider.ID = costCenter.ID
	cr.Status.AtProvider.Name = costCenter.Name
	cr.Status.AtProvider.State = costCenter.State

	if costCenter.Resources != nil {
		cr.Status.AtProvider.Resources = make([]v1alpha1.CostCenterResource, len(costCenter.Resources))
		for i, res := range costCenter.Resources {
			cr.Status.AtProvider.Resources[i] = v1alpha1.CostCenterResource{
				Type: res.Type,
				Name: res.Name,
			}
		}
	}

	if costCenter.ID != nil {
		meta.SetExternalName(cr, *costCenter.ID)
	}
}

// isUpToDate checks if the external resource matches the desired state
func (e *external) isUpToDate(costCenter *CostCenter, spec v1alpha1.CostCenterParameters) bool {
	if spec.Name != nil && costCenter.Name != nil {
		return *spec.Name == *costCenter.Name
	}
	return true
}

// isResourceActive checks if the cost center is in an active (non-deleted) state
func (e *external) isResourceActive(costCenter *CostCenter) bool {
	if costCenter.State == nil {
		return true
	}
	return *costCenter.State != "deleted"
}

func (r *DirectCostCenterReconciler) getExternalClient(ctx context.Context, cr *v1alpha1.CostCenter) (*external, error) {
	pc := &apisv1beta1.ProviderConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	cd := pc.Spec.Credentials
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, r.Client, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	type githubCreds struct {
		Token   *string `json:"token,omitempty"`
		BaseURL *string `json:"base_url,omitempty"`
	}

	var creds githubCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, errors.Wrap(err, "failed to parse GitHub credentials JSON")
	}

	token := ""
	if creds.Token != nil {
		token = *creds.Token
	}

	if token == "" {
		return nil, errors.New("GitHub token is required but not provided in credentials")
	}

	baseURL := "https://api.github.com"
	if creds.BaseURL != nil && *creds.BaseURL != "" {
		baseURL = *creds.BaseURL
	}

	svc := r.newServiceFn(ctx, token, baseURL)
	return &external{service: svc}, nil
}
