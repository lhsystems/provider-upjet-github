/*
Copyright 2022 Upbound Inc.
*/

package costcenter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/upjet/v2/pkg/controller"

	"github.com/crossplane-contrib/provider-upjet-github/apis/cluster/enterprise/v1alpha1"
)

const (
	errNotCostCenter    = "managed resource is not a CostCenter custom resource"
	errTrackPCUsage     = "cannot track ProviderConfig usage"
	errGetPC            = "cannot get ProviderConfig"
	errGetCreds         = "cannot get credentials"
	errNewClient        = "cannot create new GitHub client"
	errCreateCostCenter = "cannot create cost center"
	errGetCostCenter    = "cannot get cost center"
	errUpdateCostCenter = "cannot update cost center"
	errDeleteCostCenter = "cannot delete cost center"
)

// Setup adds a controller that reconciles CostCenter managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := "costcenter-direct"

	reconciler := &DirectCostCenterReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Logger:       o.Logger.WithValues("controller", name),
		newServiceFn: newGitHubService,
		recorder:     event.NewAPIRecorder(mgr.GetEventRecorderFor(name)),
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.CostCenter{}).
		Complete(reconciler)
}

// CostCenter represents a GitHub cost center
type CostCenter struct {
	ID        *string    `json:"id,omitempty"`
	Name      *string    `json:"name,omitempty"`
	State     *string    `json:"state,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
}

// Resource represents a resource associated with a cost center
type Resource struct {
	Type *string `json:"type,omitempty"`
	Name *string `json:"name,omitempty"`
}

// GitHubService defines the interface for GitHub cost center operations
type GitHubService interface {
	CreateCostCenter(ctx context.Context, enterprise, name string) (*CostCenter, error)
	GetCostCenter(ctx context.Context, enterprise, costCenterID string) (*CostCenter, error)
	ListCostCenters(ctx context.Context, enterprise string) ([]CostCenter, error)
	UpdateCostCenter(ctx context.Context, enterprise, costCenterID, name string) (*CostCenter, error)
	DeleteCostCenter(ctx context.Context, enterprise, costCenterID string) error
	AddResourcesToCostCenter(ctx context.Context, enterprise string, costCenterID string, resourceType string, resources []string) error
	RemoveResourcesFromCostCenter(ctx context.Context, enterprise string, costCenterID string, resourceType string, resources []string) error
}

// gitHubService implements GitHubService using raw HTTP requests
type gitHubService struct {
	token   string
	baseURL string
	client  *http.Client
}

// AddResourcesToCostCenter adds organizations or repositories to a cost center
func (s *gitHubService) AddResourcesToCostCenter(ctx context.Context, enterprise string, costCenterID string, resourceType string, resources []string) error {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers/%s/resource", enterprise, costCenterID)
	var req map[string]interface{}
	switch resourceType {
	case "Organization":
		req = map[string]interface{}{"organizations": resources}
	case "Repo":
		req = map[string]interface{}{"repositories": resources}
	case "User":
		req = map[string]interface{}{"users": resources}
	default:
		req = map[string]interface{}{resourceType: resources}
	}
	resp, err := s.makeRequest(ctx, "POST", path, req)
	if err != nil {
		return fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}
	return nil
}

// RemoveResourcesFromCostCenter removes organizations or repositories from a cost center
func (s *gitHubService) RemoveResourcesFromCostCenter(ctx context.Context, enterprise string, costCenterID string, resourceType string, resources []string) error {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers/%s/resource", enterprise, costCenterID)
	var req map[string]interface{}
	switch resourceType {
	case "Organization":
		req = map[string]interface{}{"organizations": resources}
	case "Repo":
		req = map[string]interface{}{"repositories": resources}
	case "User":
		req = map[string]interface{}{"users": resources}
	default:
		req = map[string]interface{}{resourceType: resources}
	}
	resp, err := s.makeRequest(ctx, "DELETE", path, req)
	if err != nil {
		return fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}
	return nil
}

func newGitHubService(_ context.Context, token string, baseURL string) GitHubService {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &gitHubService{
		token:   token,
		baseURL: baseURL,
		client:  &http.Client{},
	}
}

func (s *gitHubService) makeRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var reqBody *bytes.Buffer
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	} else {
		reqBody = &bytes.Buffer{}
	}

	url := fmt.Sprintf("%s/%s", strings.TrimRight(s.baseURL, "/"), path)

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	return resp, nil
}

func (s *gitHubService) CreateCostCenter(ctx context.Context, enterprise, name string) (*CostCenter, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers", enterprise)
	req := map[string]string{"name": name}

	resp, err := s.makeRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bodyBytes = []byte("failed to read response body")
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}

	var costCenter CostCenter
	if err := json.Unmarshal(bodyBytes, &costCenter); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	return &costCenter, nil
}

func (s *gitHubService) GetCostCenter(ctx context.Context, enterprise, costCenterID string) (*CostCenter, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers/%s", enterprise, costCenterID)

	resp, err := s.makeRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{Message: "cost center not found"}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}

	var costCenter CostCenter
	if err := json.Unmarshal(bodyBytes, &costCenter); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	return &costCenter, nil
}

func (s *gitHubService) ListCostCenters(ctx context.Context, enterprise string) ([]CostCenter, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers", enterprise)

	resp, err := s.makeRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}

	type ListResponse struct {
		CostCenters []CostCenter `json:"costCenters"`
	}

	var response ListResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	return response.CostCenters, nil
}

func (s *gitHubService) UpdateCostCenter(ctx context.Context, enterprise, costCenterID, name string) (*CostCenter, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers/%s", enterprise, costCenterID)
	req := map[string]string{"name": name}

	resp, err := s.makeRequest(ctx, "PATCH", path, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{Message: "cost center not found for update"}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var costCenters []CostCenter
	if err := json.Unmarshal(bodyBytes, &costCenters); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	if len(costCenters) == 0 {
		return nil, &NotFoundError{Message: "cost center not found in update response"}
	}

	return &costCenters[0], nil
}

func (s *gitHubService) DeleteCostCenter(ctx context.Context, enterprise, costCenterID string) error {
	path := fmt.Sprintf("enterprises/%s/settings/billing/cost-centers/%s", enterprise, costCenterID)

	resp, err := s.makeRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("failed to delete cost center: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bodyBytes = []byte("failed to read response body")
	}

	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Message: "cost center not found"}
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("GitHub API error: %d - Response: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// NotFoundError represents a 404 error
type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "resource not found"
}

// containsFinalizer checks if a finalizer is present in the slice
func containsFinalizer(finalizers []string, finalizer string) bool {
	for _, f := range finalizers {
		if f == finalizer {
			return true
		}
	}
	return false
}

// DirectCostCenterReconciler bypasses the managed reconciler pattern to avoid upjet interference
type DirectCostCenterReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Logger       logging.Logger
	newServiceFn func(ctx context.Context, token string, baseURL string) GitHubService
	recorder     event.Recorder
}

// Reconcile handles the reconciliation loop for CostCenter resources.
func (r *DirectCostCenterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var costCenter v1alpha1.CostCenter
	if err := r.Get(ctx, req.NamespacedName, &costCenter); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		r.Logger.Info("Failed to get CostCenter resource", "error", err)
		return ctrl.Result{}, err
	}

	if costCenter.GetDeletionTimestamp() != nil {
		return r.handleDeletion(ctx, &costCenter)
	}

	result, err := r.ensureFinalizer(ctx, &costCenter)
	if err != nil || result.Requeue {
		return result, err
	}

	return r.reconcileResource(ctx, req, &costCenter)
}

func (r *DirectCostCenterReconciler) ensureFinalizer(ctx context.Context, costCenter *v1alpha1.CostCenter) (ctrl.Result, error) {
	const finalizer = "finalizer.managedresource.crossplane.io"
	if !containsFinalizer(costCenter.GetFinalizers(), finalizer) {
		costCenter.SetFinalizers(append(costCenter.GetFinalizers(), finalizer))
		if err := r.Update(ctx, costCenter); err != nil {
			r.Logger.Info("Failed to add finalizer", "error", err)
			r.recorder.Event(costCenter, event.Warning("FinalizerError", err))
			return ctrl.Result{}, err
		}
		r.recorder.Event(costCenter, event.Normal("FinalizerAdded", "Successfully added finalizer"))
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DirectCostCenterReconciler) reconcileResource(ctx context.Context, req ctrl.Request, costCenter *v1alpha1.CostCenter) (ctrl.Result, error) {
	externalClient, err := r.getExternalClient(ctx, costCenter)
	if err != nil {
		r.Logger.Info("Failed to create external client", "error", err)
		r.recorder.Event(costCenter, event.Warning("ClientError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	observation, err := r.observeResource(ctx, req, externalClient)
	if err != nil {
		r.recorder.Event(costCenter, event.Warning("ObservationError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	err = r.handleResourceState(ctx, externalClient, costCenter, observation)
	if err != nil {
		r.recorder.Event(costCenter, event.Warning("ResourceStateError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	err = r.updateResourceStatus(ctx, req, externalClient, costCenter, observation)
	if err != nil {
		r.recorder.Event(costCenter, event.Warning("StatusUpdateError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	if observation.ResourceExists {
		if observation.ResourceUpToDate {
			r.recorder.Event(costCenter, event.Normal("Synced", "Cost center is up to date"))
		} else {
			r.recorder.Event(costCenter, event.Normal("Updated", "Cost center has been updated"))
		}
	} else {
		r.recorder.Event(costCenter, event.Normal("Created", "Cost center has been created"))
	}

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func (r *DirectCostCenterReconciler) observeResource(ctx context.Context, req ctrl.Request, externalClient *external) (ExternalObservation, error) {
	var costCenter v1alpha1.CostCenter
	if err := r.Get(ctx, req.NamespacedName, &costCenter); err != nil {
		r.Logger.Info("Failed to get latest CostCenter resource", "error", err)
		return ExternalObservation{}, err
	}

	observation, err := externalClient.Observe(ctx, &costCenter)
	if err != nil {
		r.Logger.Info("Failed to observe external resource", "error", err)
		return ExternalObservation{}, err
	}

	return observation, nil
}

func (r *DirectCostCenterReconciler) handleResourceState(ctx context.Context, externalClient *external, costCenter *v1alpha1.CostCenter, observation ExternalObservation) error {
	if !observation.ResourceExists {
		_, err := externalClient.Create(ctx, costCenter)
		if err != nil {
			r.Logger.Info("Failed to create external resource", "error", err)
			return err
		}
	} else if !observation.ResourceUpToDate {
		_, err := externalClient.Update(ctx, costCenter)
		if err != nil {
			r.Logger.Info("Failed to update external resource", "error", err)
			return err
		}
	}
	return nil
}

func (r *DirectCostCenterReconciler) updateResourceStatus(ctx context.Context, req ctrl.Request, externalClient *external, costCenter *v1alpha1.CostCenter, observation ExternalObservation) error {
	costCenter.Status.SetConditions(xpv1.Available())
	if observation.ResourceUpToDate {
		costCenter.Status.SetConditions(xpv1.Available().WithMessage("Resource is up to date"))
	}

	if observation.ResourceExists {
		if observation.ResourceUpToDate {
			costCenter.Status.SetConditions(xpv1.ReconcileSuccess())
		} else {
			costCenter.Status.SetConditions(xpv1.ReconcileError(errors.New("resource exists but is not up to date")))
		}
	} else {
		costCenter.Status.SetConditions(xpv1.ReconcileError(errors.New("resource does not exist yet")))
	}

	if err := r.Status().Update(ctx, costCenter); err != nil {
		r.Logger.Info("Failed to update status", "error", err)
		return err
	}

	var latestCostCenter v1alpha1.CostCenter
	if err := r.Get(ctx, req.NamespacedName, &latestCostCenter); err != nil {
		r.Logger.Info("Failed to get latest CostCenter resource for metadata update", "error", err)
		return err
	}

	_, err := externalClient.Observe(ctx, &latestCostCenter)
	if err != nil {
		r.Logger.Info("Failed to re-observe for ExternalName setting", "error", err)
		return err
	}

	if err := r.Update(ctx, &latestCostCenter); err != nil {
		r.Logger.Info("Failed to update resource metadata", "error", err)
		return err
	}

	return nil
}

func (r *DirectCostCenterReconciler) handleDeletion(ctx context.Context, costCenter *v1alpha1.CostCenter) (ctrl.Result, error) {
	const finalizer = "finalizer.managedresource.crossplane.io"

	if !containsFinalizer(costCenter.GetFinalizers(), finalizer) {
		return ctrl.Result{}, nil
	}

	externalClient, err := r.getExternalClient(ctx, costCenter)
	if err != nil {
		r.Logger.Info("Failed to create external client for deletion", "error", err)
		r.recorder.Event(costCenter, event.Warning("DeletionClientError", err))
	} else {
		err = externalClient.Delete(ctx, costCenter)
		if err != nil {
			r.Logger.Info("Failed to delete external resource", "error", err)
			r.recorder.Event(costCenter, event.Warning("DeletionError", err))
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		r.recorder.Event(costCenter, event.Normal("Deleted", "Cost center has been deleted from GitHub"))
	}

	finalizers := costCenter.GetFinalizers()
	var newFinalizers []string
	for _, f := range finalizers {
		if f != finalizer {
			newFinalizers = append(newFinalizers, f)
		}
	}
	costCenter.SetFinalizers(newFinalizers)

	if err := r.Update(ctx, costCenter); err != nil {
		r.Logger.Info("Failed to remove finalizer", "error", err)
		r.recorder.Event(costCenter, event.Warning("FinalizerRemovalError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	r.recorder.Event(costCenter, event.Normal("FinalizerRemoved", "Successfully removed finalizer"))
	return ctrl.Result{}, nil
}
