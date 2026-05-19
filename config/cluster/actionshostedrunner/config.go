package actionshostedrunner

import "github.com/crossplane/upjet/v2/pkg/config"

// Configure github_actions_hosted_runner resource.
func Configure(p *config.Provider) {
	p.AddResourceConfigurator("github_actions_hosted_runner", func(r *config.Resource) {
		r.ShortGroup = "actions"

		// runner_group_id is a Terraform schema.TypeInt field (*int64 in Go),
		// but upjet's reference resolver template only supports *string fields
		// (it emits reference.FromPtrValue / ToPtrValue). The provider-metadata
		// auto-injects a cross-resource reference for runner_group_id from the
		// example HCL, which then produces a generated resolver that does not
		// compile. Drop the auto-injected reference so users supply the int ID
		// directly.
		delete(r.References, "runner_group_id")
	})
}
