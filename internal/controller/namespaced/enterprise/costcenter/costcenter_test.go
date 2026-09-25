package costcenter

import (
	"context"
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	namespaced "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced"
	enterprise "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/enterprise/v1alpha1"
	providerconfig "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/v1beta1"
	clustercc "github.com/crossplane-contrib/provider-upjet-github/internal/controller/cluster/enterprise/costcenter"
)

const testNamespace = "team-a"

func TestServiceUsesNamespacedProviderConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := namespaced.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	pc := &providerconfig.ProviderConfig{}
	pc.Name = "github"
	pc.Namespace = testNamespace
	pc.Spec.Credentials.Source = xpv1.CredentialsSource("None")
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).Build()
	cr := &enterprise.CostCenter{}
	cr.Name = "cost-center"
	cr.Namespace = testNamespace
	cr.Spec.ProviderConfigReference = &xpv1.ProviderConfigReference{Kind: "ProviderConfig", Name: "github"}

	r := &reconciler{
		Client: kubeClient,
		newService: func(_ context.Context, credentials clustercc.GithubCredentials) (clustercc.GitHubService, error) {
			return nil, nil
		},
	}
	got, err := r.providerConfig(context.Background(), cr)
	if err != nil {
		t.Fatalf("providerConfig() error = %v", err)
	}
	if got.Name != pc.Name || got.Namespace != pc.Namespace {
		t.Fatalf("providerConfig() = %s/%s, want %s/%s", got.Namespace, got.Name, pc.Namespace, pc.Name)
	}
}

func TestDeleteRetainsFinalizerWhenProviderConfigUnavailable(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := namespaced.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	cr := &enterprise.CostCenter{}
	cr.Name = "cost-center"
	cr.Namespace = testNamespace
	cr.Finalizers = []string{finalizer}
	cr.Spec.ProviderConfigReference = &xpv1.ProviderConfigReference{Kind: "ProviderConfig", Name: "github"}
	cr.Status.AtProvider.ID = func() *string { value := "cost-center-id"; return &value }()

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()
	r := &reconciler{Client: kubeClient}

	if _, err := r.delete(context.Background(), cr); err == nil {
		t.Fatal("delete() error = nil, want ProviderConfig lookup error")
	}

	var got enterprise.CostCenter
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(cr), &got); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != finalizer {
		t.Fatalf("finalizers = %v, want %q", got.Finalizers, finalizer)
	}
}

func TestProviderConfigUsageTracking(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := namespaced.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	cr := &enterprise.CostCenter{}
	cr.APIVersion = enterprise.CRDGroupVersion.String()
	cr.Kind = enterprise.CostCenterKind
	cr.Name = "cost-center"
	cr.Namespace = testNamespace
	cr.UID = types.UID("cost-center-uid")
	cr.Spec.ProviderConfigReference = &xpv1.ProviderConfigReference{Kind: "ProviderConfig", Name: "github"}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	tracker := resource.NewProviderConfigUsageTracker(kubeClient, &providerconfig.ProviderConfigUsage{})
	if err := tracker.Track(context.Background(), cr); err != nil {
		t.Fatalf("Track() error = %v", err)
	}

	usage := &providerconfig.ProviderConfigUsage{}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: string(cr.UID), Namespace: cr.Namespace}, usage); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := usage.GetProviderConfigReference(); got.Name != "github" || got.Kind != "ProviderConfig" {
		t.Fatalf("ProviderConfigReference = %#v, want ProviderConfig/github", got)
	}
}

func TestServiceUsesCostCenterNamespaceForCredentialSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := namespaced.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	pc := &providerconfig.ProviderConfig{}
	pc.Name = "github"
	pc.Namespace = testNamespace
	pc.Spec.Credentials.Source = xpv1.CredentialsSourceSecret
	pc.Spec.Credentials.SecretRef = &xpv1.SecretKeySelector{
		SecretReference: xpv1.SecretReference{Name: "credentials", Namespace: "other-namespace"},
		Key:             "credentials",
	}
	secret := &corev1.Secret{}
	secret.Name = "credentials"
	secret.Namespace = testNamespace
	secret.Data = map[string][]byte{"credentials": []byte(`{"token":"token"}`)}
	cr := &enterprise.CostCenter{}
	cr.Name = "cost-center"
	cr.Namespace = testNamespace
	cr.Spec.ProviderConfigReference = &xpv1.ProviderConfigReference{Kind: "ProviderConfig", Name: "github"}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc, secret).Build()
	r := &reconciler{
		Client: kubeClient,
		newService: func(_ context.Context, credentials clustercc.GithubCredentials) (clustercc.GitHubService, error) {
			if credentials.Token == nil || *credentials.Token != "token" {
				t.Fatalf("credentials.Token = %v, want token from team-a secret", credentials.Token)
			}
			return nil, nil
		},
	}
	if _, err := r.service(context.Background(), cr); err != nil {
		t.Fatalf("service() error = %v", err)
	}
}

func TestAllowsManagementAction(t *testing.T) {
	cr := &enterprise.CostCenter{}
	cr.Spec.ManagementPolicies = []xpv1.ManagementAction{xpv1.ManagementActionObserve}

	if !allows(cr, xpv1.ManagementActionObserve) {
		t.Fatal("allows(Observe) = false, want true")
	}
	for _, action := range []xpv1.ManagementAction{xpv1.ManagementActionCreate, xpv1.ManagementActionUpdate, xpv1.ManagementActionDelete} {
		if allows(cr, action) {
			t.Fatalf("allows(%s) = true, want false", action)
		}
	}
}
