package clientperm

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
)

// GrantPreview is inert data, grouped by covered procedure rather than an
// execution schedule. Descriptions retain conditions and nested procedure calls.
type GrantPreview struct {
	Grant      standinggrant.Grant `json:"grant"`
	Procedures []ProcedurePreview  `json:"procedures"`
	Disclaimer string              `json:"disclaimer"`
}

type ProcedurePreview struct {
	ID               string            `json:"id"`
	Revision         int               `json:"revision"`
	WorkingDirectory string            `json:"working_directory"`
	Steps            []StepPreview     `json:"steps"`
	DeclaredEffects  []string          `json:"declared_effects"`
	Validations      []string          `json:"validations"`
	ParameterValues  map[string]string `json:"parameter_values"`
}

type StepPreview struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	State        string   `json:"state"`
	Argv         []string `json:"argv,omitempty"`
	Command      string   `json:"command,omitempty"`
	Missing      []string `json:"missing_parameters,omitempty"`
	CheckArgv    []string `json:"codex_check_argv,omitempty"`
	CheckCommand string   `json:"codex_check_command,omitempty"`
}

func parameterValues(procedure procedures.Procedure, grant standinggrant.Grant) map[string]string {
	values := map[string]string{}
	for _, parameter := range procedure.Parameters {
		if parameter.Default != "" {
			values[parameter.Name] = parameter.Default
		}
	}
	for name, value := range grant.Parameters {
		values[name] = value
	}
	return values
}

// Preview renders only active, fully bound commands. It never invokes Codex,
// executes a procedure, changes permissions, or treats a rule as authorization.
func Preview(ctx context.Context, db *sql.DB, projectID, id string, rules []string) (GrantPreview, error) {
	grant, err := (standinggrant.Service{DB: db}).Get(ctx, projectID, id)
	if err != nil {
		return GrantPreview{}, err
	}
	result := GrantPreview{Grant: grant, Procedures: []ProcedurePreview{}, Disclaimer: "Read-only authorization evidence and command preview; nothing was executed or checked. Grant attribution, state, expiry and covered revisions come from the recorded user grant. Declared effects and validations come from those exact procedure revisions; parameter values apply defaults then grant overrides. A parameter value alone does not authorize a data transfer. Missing data scope or destination remains unknown; do not infer broader consent. An active matching grant needs no repeat user confirmation, but cannot override a runtime denial. Procedures are grouped, not flattened into an execution schedule. Follow step conditions and project-relative working directories. Preserve bound paths and each argv exactly. Rule checks cover only the supplied files, not effective runtime authorization. Inactive grants emit no commands."}
	store := procedures.Service{DB: db}
	for _, covered := range grant.Covers {
		procedure, err := store.Get(ctx, projectID, covered.ProcedureID, covered.Revision)
		if err != nil {
			return GrantPreview{}, err
		}
		if procedure == nil {
			return GrantPreview{}, fmt.Errorf("missing covered procedure %s@%d", covered.ProcedureID, covered.Revision)
		}
		p := ProcedurePreview{ID: procedure.ID, Revision: procedure.Revision, WorkingDirectory: procedure.WorkingDirectory, Steps: []StepPreview{}}
		values := parameterValues(*procedure, grant)
		p.DeclaredEffects = procedure.DeclaredEffects
		p.Validations = procedure.Validations
		p.ParameterValues = values
		for _, step := range procedure.Steps {
			s := StepPreview{ID: step.ID, Description: step.Description, State: "description_only"}
			if len(step.ArgvTemplate) > 0 {
				for _, arg := range step.ArgvTemplate {
					for _, match := range placeholder.FindAllStringSubmatch(arg, -1) {
						if _, ok := values[match[1]]; !ok {
							s.Missing = appendUnique(s.Missing, match[1])
						}
					}
				}
				switch {
				case grant.State != standinggrant.Active:
					s.State = "inactive_grant"
				case len(s.Missing) > 0:
					s.State = "unresolved"
				default:
					s.State = "bound"
					rendered := Render(step.ArgvTemplate, values)
					s.Argv, s.Command = rendered.Args, rendered.Command()
					if len(rules) > 0 {
						s.CheckArgv = []string{"codex", "execpolicy", "check"}
						for _, path := range rules {
							s.CheckArgv = append(s.CheckArgv, "--rules", path)
						}
						s.CheckArgv = append(s.CheckArgv, "--")
						s.CheckArgv = append(s.CheckArgv, s.Argv...)
						s.CheckCommand = (Rendered{Args: s.CheckArgv, Exact: true}).Command()
					}
				}
			}
			p.Steps = append(p.Steps, s)
		}
		result.Procedures = append(result.Procedures, p)
	}
	return result, nil
}
