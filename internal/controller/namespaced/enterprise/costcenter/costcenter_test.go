package costcenter

import (
	"context"
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	namespaced "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced"
	enterprise "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/enterprise/v1alpha1"
	providerconfig "github.com/crossplane-contrib/provider-upjet-github/apis/namespaced/v1beta1"
	clustercc "github.com/crossplane-contrib/provider-upjet-github/internal/controller/cluster/enterprise/costcenter"
)

func TestServiceUsesNamespacedProviderConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := namespaced.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	pc := &providerconfig.ProviderConfig{}
	pc.Name = "github"
	pc.Namespace = "team-a"
	pc.Spec.Credentials.Source = xpv1.CredentialsSource("None")
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pc).Build()
	cr := &enterprise.CostCenter{}
	cr.Name = "cost-center"
	cr.Namespace = "team-a"
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
	cr.Namespace = "team-a"
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
