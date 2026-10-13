/*
Copyright 2022 Upbound Inc.
*/

package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
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
	errNotBudget = "managed resource is not a Budget custom resource"
	errGetPC     = "cannot get ProviderConfig"
	errGetCreds  = "cannot get credentials"
	errNewClient = "cannot create new GitHub client"
	finalizer    = "finalizer.managedresource.crossplane.io"
)

// Setup adds a controller that reconciles Budget managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := "budget-direct"

	reconciler := &DirectBudgetReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Logger:       o.Logger.WithValues("controller", name),
		newServiceFn: NewGitHubService,
		recorder:     event.NewAPIRecorder(mgr.GetEventRecorderFor(name)),
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.Budget{}).
		Complete(reconciler)
}

// Budget represents a GitHub enterprise budget as returned by the GitHub API.
type Budget struct {
	ID                  *string         `json:"id,omitempty"`
	BudgetScope         *string         `json:"budget_scope,omitempty"`
	BudgetEntityName    *string         `json:"budget_entity_name,omitempty"`
	User                *string         `json:"user,omitempty"`
	BudgetType          *string         `json:"budget_type,omitempty"`
	BudgetProductSKU    *string         `json:"budget_product_sku,omitempty"`
	BudgetAmount        *int64          `json:"budget_amount,omitempty"`
	PreventFurtherUsage *bool           `json:"prevent_further_usage,omitempty"`
	BudgetAlerting      *BudgetAlerting `json:"budget_alerting,omitempty"`
	ExpiresAt           *string         `json:"expires_at,omitempty"`
	ConsumedAmount      *float64        `json:"consumed_amount,omitempty"`
}

// BudgetAlerting represents the alerting configuration of a budget.
type BudgetAlerting struct {
	WillAlert       *bool    `json:"will_alert,omitempty"`
	AlertRecipients []string `json:"alert_recipients,omitempty"`
}

// GitHubService defines the interface for GitHub budget operations.
type GitHubService interface {
	CreateBudget(ctx context.Context, enterprise string, budget *Budget) (*Budget, error)
	GetBudget(ctx context.Context, enterprise, budgetID string) (*Budget, error)
	ListBudgets(ctx context.Context, enterprise string) ([]Budget, error)
	UpdateBudget(ctx context.Context, enterprise, budgetID string, budget *Budget) (*Budget, error)
	DeleteBudget(ctx context.Context, enterprise, budgetID string) error
}

// gitHubService implements GitHubService using raw HTTP requests against the GitHub REST API.
type gitHubService struct {
	baseURL string
	client  *http.Client
}

// GithubAppAuth holds the configuration required to authenticate as a GitHub App installation.
type GithubAppAuth struct {
	ID             string `json:"id"`
	InstallationID string `json:"installation_id"`
	PEMFile        string `json:"pem_file"`
}

// GithubCredentials holds the GitHub authentication credentials, supporting
// either a Personal Access Token or GitHub App installation authentication.
type GithubCredentials struct {
	Token   *string          `json:"token,omitempty"`
	BaseURL *string          `json:"base_url,omitempty"`
	AppAuth *[]GithubAppAuth `json:"app_auth,omitempty"`
}

// bearerAuthTransport authenticates requests using a bearer token (PAT).
type bearerAuthTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// NewGitHubService creates a GitHubService authenticated either via a GitHub
// App installation or a Personal Access Token, depending on the supplied credentials.
func NewGitHubService(_ context.Context, creds GithubCredentials) (GitHubService, error) {
	baseURL := "https://api.github.com"
	if creds.BaseURL != nil && *creds.BaseURL != "" {
		baseURL = *creds.BaseURL
	}

	var transport http.RoundTripper
	switch {
	case creds.AppAuth != nil:
		if len(*creds.AppAuth) != 1 {
			return nil, errors.New("GitHub app_auth must contain exactly one configuration")
		}
		appAuth := (*creds.AppAuth)[0]
		appID, err := strconv.ParseInt(appAuth.ID, 10, 64)
		if err != nil {
			return nil, errors.Wrap(err, "GitHub app_auth id must be an integer")
		}
		installationID, err := strconv.ParseInt(appAuth.InstallationID, 10, 64)
		if err != nil {
			return nil, errors.Wrap(err, "GitHub app_auth installation_id must be an integer")
		}
		installationTransport, err := ghinstallation.New(http.DefaultTransport, appID, installationID, []byte(appAuth.PEMFile))
		if err != nil {
			return nil, errors.Wrap(err, "cannot configure GitHub App authentication")
		}
		installationTransport.BaseURL = strings.TrimRight(baseURL, "/")
		transport = installationTransport
	case creds.Token != nil && *creds.Token != "":
		transport = &bearerAuthTransport{token: *creds.Token, base: http.DefaultTransport}
	default:
		return nil, errors.New("GitHub token or app_auth is required but not provided in credentials")
	}

	return &gitHubService{
		baseURL: baseURL,
		client:  &http.Client{Transport: transport},
	}, nil
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

func (s *gitHubService) CreateBudget(ctx context.Context, enterprise string, budget *Budget) (*Budget, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/budgets", enterprise)

	resp, err := s.makeRequest(ctx, http.MethodPost, path, budget)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bodyBytes = []byte("failed to read response body")
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}

	var response struct {
		Budget *Budget `json:"budget"`
	}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}
	if response.Budget == nil {
		return nil, fmt.Errorf("GitHub API response did not contain a budget - Response: %s", string(bodyBytes))
	}

	return response.Budget, nil
}

func (s *gitHubService) GetBudget(ctx context.Context, enterprise, budgetID string) (*Budget, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/budgets/%s", enterprise, budgetID)

	resp, err := s.makeRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{Message: "budget not found"}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d %s - Response: %s", resp.StatusCode, resp.Status, string(bodyBytes))
	}

	var budget Budget
	if err := json.Unmarshal(bodyBytes, &budget); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	return &budget, nil
}

func (s *gitHubService) ListBudgets(ctx context.Context, enterprise string) ([]Budget, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/budgets", enterprise)

	resp, err := s.makeRequest(ctx, http.MethodGet, path, nil)
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

	var response struct {
		Budgets []Budget `json:"budgets"`
	}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}

	return response.Budgets, nil
}

func (s *gitHubService) UpdateBudget(ctx context.Context, enterprise, budgetID string, budget *Budget) (*Budget, error) {
	path := fmt.Sprintf("enterprises/%s/settings/billing/budgets/%s", enterprise, budgetID)

	// Only budget_amount, prevent_further_usage, budget_alerting, and
	// expires_at are mutable via the update endpoint.
	updateBody := struct {
		BudgetAmount        *int64          `json:"budget_amount,omitempty"`
		PreventFurtherUsage *bool           `json:"prevent_further_usage,omitempty"`
		BudgetAlerting      *BudgetAlerting `json:"budget_alerting,omitempty"`
		ExpiresAt           *string         `json:"expires_at,omitempty"`
	}{
		BudgetAmount:        budget.BudgetAmount,
		PreventFurtherUsage: budget.PreventFurtherUsage,
		BudgetAlerting:      budget.BudgetAlerting,
		ExpiresAt:           budget.ExpiresAt,
	}

	resp, err := s.makeRequest(ctx, http.MethodPatch, path, updateBody)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bodyBytes = []byte("failed to read response body")
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{Message: "budget not found for update"}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: %d - Response: %s", resp.StatusCode, string(bodyBytes))
	}

	var response struct {
		Budget *Budget `json:"budget"`
	}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w - Response: %s", err, string(bodyBytes))
	}
	if response.Budget == nil {
		return nil, fmt.Errorf("GitHub API response did not contain a budget - Response: %s", string(bodyBytes))
	}

	return response.Budget, nil
}

func (s *gitHubService) DeleteBudget(ctx context.Context, enterprise, budgetID string) error {
	path := fmt.Sprintf("enterprises/%s/settings/billing/budgets/%s", enterprise, budgetID)

	resp, err := s.makeRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("failed to delete budget: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		bodyBytes = []byte("failed to read response body")
	}

	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Message: "budget not found"}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("GitHub API error: %d - Response: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// NotFoundError represents a 404 response from the GitHub API.
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

// allows reports whether the given management action is permitted by the
// resource's management policies.
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

// DirectBudgetReconciler bypasses the managed reconciler pattern to avoid upjet interference
type DirectBudgetReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Logger       logging.Logger
	newServiceFn func(ctx context.Context, creds GithubCredentials) (GitHubService, error)
	recorder     event.Recorder
}

// Reconcile handles the reconciliation loop for Budget resources.
func (r *DirectBudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var budget v1alpha1.Budget
	if err := r.Get(ctx, req.NamespacedName, &budget); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		r.Logger.Info("Failed to get Budget resource", "error", err)
		return ctrl.Result{}, err
	}

	if budget.GetDeletionTimestamp() != nil {
		return r.handleDeletion(ctx, &budget)
	}

	added, err := r.ensureFinalizer(ctx, &budget)
	if err != nil {
		return ctrl.Result{}, err
	}
	if added {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return r.reconcileResource(ctx, req, &budget)
}

func (r *DirectBudgetReconciler) ensureFinalizer(ctx context.Context, budget *v1alpha1.Budget) (bool, error) {
	if !containsFinalizer(budget.GetFinalizers(), finalizer) {
		budget.SetFinalizers(append(budget.GetFinalizers(), finalizer))
		if err := r.Update(ctx, budget); err != nil {
			r.Logger.Info("Failed to add finalizer", "error", err)
			r.recorder.Event(budget, event.Warning("FinalizerError", err))
			return false, err
		}
		r.recorder.Event(budget, event.Normal("FinalizerAdded", "Successfully added finalizer"))
		return true, nil
	}
	return false, nil
}

func (r *DirectBudgetReconciler) reconcileResource(ctx context.Context, req ctrl.Request, budget *v1alpha1.Budget) (ctrl.Result, error) {
	externalClient, err := r.getExternalClient(ctx, budget)
	if err != nil {
		r.Logger.Info("Failed to create external client", "error", err)
		r.recorder.Event(budget, event.Warning("ClientError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	observation, err := r.observeResource(ctx, req, externalClient)
	if err != nil {
		r.recorder.Event(budget, event.Warning("ObservationError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	if err := r.handleResourceState(ctx, externalClient, budget, observation); err != nil {
		r.recorder.Event(budget, event.Warning("ResourceStateError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	if err := r.updateResourceStatus(ctx, req, externalClient, budget, observation); err != nil {
		r.recorder.Event(budget, event.Warning("StatusUpdateError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	if observation.ResourceExists {
		if observation.ResourceUpToDate {
			r.recorder.Event(budget, event.Normal("Synced", "Budget is up to date"))
		} else {
			r.recorder.Event(budget, event.Normal("Updated", "Budget has been updated"))
		}
	} else {
		r.recorder.Event(budget, event.Normal("Created", "Budget has been created"))
	}

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func (r *DirectBudgetReconciler) observeResource(ctx context.Context, req ctrl.Request, externalClient *external) (ExternalObservation, error) {
	var budget v1alpha1.Budget
	if err := r.Get(ctx, req.NamespacedName, &budget); err != nil {
		r.Logger.Info("Failed to get latest Budget resource", "error", err)
		return ExternalObservation{}, err
	}

	observation, err := externalClient.Observe(ctx, &budget)
	if err != nil {
		r.Logger.Info("Failed to observe external resource", "error", err)
		return ExternalObservation{}, err
	}

	return observation, nil
}

func (r *DirectBudgetReconciler) handleResourceState(ctx context.Context, externalClient *external, budget *v1alpha1.Budget, observation ExternalObservation) error {
	if !observation.ResourceExists {
		if !allows(budget, xpv1.ManagementActionCreate) {
			return nil
		}
		if _, err := externalClient.Create(ctx, budget); err != nil {
			r.Logger.Info("Failed to create external resource", "error", err)
			return err
		}
	} else if !observation.ResourceUpToDate {
		if !allows(budget, xpv1.ManagementActionUpdate) {
			return nil
		}
		if _, err := externalClient.Update(ctx, budget); err != nil {
			r.Logger.Info("Failed to update external resource", "error", err)
			return err
		}
	}
	return nil
}

func (r *DirectBudgetReconciler) updateResourceStatus(ctx context.Context, req ctrl.Request, externalClient *external, budget *v1alpha1.Budget, observation ExternalObservation) error {
	budget.Status.SetConditions(xpv1.Available())
	if observation.ResourceUpToDate {
		budget.Status.SetConditions(xpv1.Available().WithMessage("Resource is up to date"))
	}

	if observation.ResourceExists {
		if observation.ResourceUpToDate {
			budget.Status.SetConditions(xpv1.ReconcileSuccess())
		} else {
			budget.Status.SetConditions(xpv1.ReconcileError(errors.New("resource exists but is not up to date")))
		}
	} else {
		budget.Status.SetConditions(xpv1.ReconcileError(errors.New("resource does not exist yet")))
	}

	if err := r.Status().Update(ctx, budget); err != nil {
		r.Logger.Info("Failed to update status", "error", err)
		return err
	}

	var latestBudget v1alpha1.Budget
	if err := r.Get(ctx, req.NamespacedName, &latestBudget); err != nil {
		r.Logger.Info("Failed to get latest Budget resource for metadata update", "error", err)
		return err
	}

	_, err := externalClient.Observe(ctx, &latestBudget)
	if err != nil {
		r.Logger.Info("Failed to re-observe for ExternalName setting", "error", err)
		return err
	}

	if err := r.Update(ctx, &latestBudget); err != nil {
		r.Logger.Info("Failed to update resource metadata", "error", err)
		return err
	}

	return nil
}

func (r *DirectBudgetReconciler) handleDeletion(ctx context.Context, budget *v1alpha1.Budget) (ctrl.Result, error) {
	if !containsFinalizer(budget.GetFinalizers(), finalizer) {
		return ctrl.Result{}, nil
	}

	if allows(budget, xpv1.ManagementActionDelete) {
		externalClient, err := r.getExternalClient(ctx, budget)
		if err != nil {
			r.Logger.Info("Failed to create external client for deletion", "error", err)
			r.recorder.Event(budget, event.Warning("DeletionClientError", err))
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}

		if err := externalClient.Delete(ctx, budget); err != nil {
			r.Logger.Info("Failed to delete external resource", "error", err)
			r.recorder.Event(budget, event.Warning("DeletionError", err))
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		r.recorder.Event(budget, event.Normal("Deleted", "Budget has been deleted from GitHub"))
	}

	finalizers := budget.GetFinalizers()
	newFinalizers := make([]string, 0, len(finalizers))
	for _, f := range finalizers {
		if f != finalizer {
			newFinalizers = append(newFinalizers, f)
		}
	}
	budget.SetFinalizers(newFinalizers)

	if err := r.Update(ctx, budget); err != nil {
		r.Logger.Info("Failed to remove finalizer", "error", err)
		r.recorder.Event(budget, event.Warning("FinalizerRemovalError", err))
		return ctrl.Result{RequeueAfter: time.Minute}, err
	}

	r.recorder.Event(budget, event.Normal("FinalizerRemoved", "Successfully removed finalizer"))
	return ctrl.Result{}, nil
}
