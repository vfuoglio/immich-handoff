package immich

import (
	"context"
	"encoding/json"
	"reflect"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// WorkflowSpec describes the one workflow this app owns in a source account.
type WorkflowSpec struct {
	Name         string
	TagID        openapi_types.UUID // fires when this tag is added
	WebhookURL   string
	HeaderName   string
	HeaderSecret string
}

func (w WorkflowSpec) steps() []WorkflowStepDto {
	filter := map[string]interface{}{"tags": []string{w.TagID.String()}, "matching": "any"}
	hook := map[string]interface{}{"url": w.WebhookURL, "method": "POST", "headerName": w.HeaderName, "headerValue": w.HeaderSecret}
	t := true
	return []WorkflowStepDto{
		{Method: "immich-plugin-core#assetTagFilter", Config: &filter, Enabled: &t},
		{Method: "immich-plugin-core#webhook", Config: &hook, Enabled: &t},
	}
}

// EnsureWorkflow creates the workflow if missing, repairs it if its steps drifted (for example the
// webhook URL or secret changed), and enables it. It reports what it did: "created", "updated" or "ok".
func (a *API) EnsureWorkflow(ctx context.Context, w WorkflowSpec) (string, error) {
	name := w.Name
	list, err := a.c.SearchWorkflowsWithResponse(ctx, &SearchWorkflowsParams{Name: &name})
	if err != nil {
		return "", err
	}
	if list.JSON200 == nil {
		return "", statusErr("searchWorkflows", list.StatusCode(), list.Body)
	}
	want := w.steps()
	for _, have := range *list.JSON200 {
		if have.Name == nil || *have.Name != w.Name {
			continue
		}
		if have.Enabled && have.Trigger == AssetTagged && sameSteps(have.Steps, want) {
			return "ok", nil
		}
		enabled, trig := true, AssetTagged
		u, err := a.c.UpdateWorkflowWithResponse(ctx, have.Id, UpdateWorkflowJSONRequestBody{Enabled: &enabled, Trigger: &trig, Steps: &want})
		if err != nil {
			return "", err
		}
		if u.JSON200 == nil {
			return "", statusErr("updateWorkflow", u.StatusCode(), u.Body)
		}
		return "updated", nil
	}
	enabled, logging := true, true
	desc := "Sends photos tagged for sharing to the family account. Managed by immich-handoff."
	c, err := a.c.CreateWorkflowWithResponse(ctx, CreateWorkflowJSONRequestBody{
		Name: &name, Description: &desc, Trigger: AssetTagged, Enabled: &enabled, Logging: &logging, Steps: &want,
	})
	if err != nil {
		return "", err
	}
	if c.JSON201 == nil {
		return "", statusErr("createWorkflow", c.StatusCode(), c.Body)
	}
	return "created", nil
}

// sameSteps compares method and config through JSON so number/slice typing differences do not matter.
func sameSteps(have, want []WorkflowStepDto) bool {
	if len(have) != len(want) {
		return false
	}
	norm := func(s WorkflowStepDto) (string, any) {
		var cfg any
		b, _ := json.Marshal(s.Config)
		_ = json.Unmarshal(b, &cfg)
		return s.Method, cfg
	}
	for i := range have {
		m1, c1 := norm(have[i])
		m2, c2 := norm(want[i])
		if m1 != m2 || !reflect.DeepEqual(c1, c2) {
			return false
		}
		if have[i].Enabled != nil && !*have[i].Enabled {
			return false
		}
	}
	return true
}
