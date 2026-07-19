// Package querytool exposes bounded, read-only research metadata to an
// authenticated analyst model. It never returns artifact bytes or host paths.
package querytool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

const (
	defaultListLimit    = 10
	maxListLimit        = 20
	maxQueryOutputBytes = 64 << 10
)

// CampaignAccess is the authorization boundary implemented by the deterministic
// research service. The model cannot select a principal through tool arguments.
type CampaignAccess interface {
	CanReadCampaign(context.Context, domain.Principal, string) (bool, error)
}

// Tool reads durable research metadata for the principal captured at request
// construction time. It intentionally depends on the metadata store, not the
// worker or mutation service.
type Tool struct {
	store     *store.Store
	access    CampaignAccess
	principal domain.Principal
}

type arguments struct {
	Operation  string `json:"operation"`
	ObjectType string `json:"object_type"`
	ID         string `json:"id,omitempty"`
	CampaignID string `json:"campaign_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// New binds a query tool to one authenticated request principal.
func New(repository *store.Store, access CampaignAccess, principal domain.Principal) (*Tool, error) {
	if repository == nil || access == nil || strings.TrimSpace(principal.ID) == "" {
		return nil, errors.New("research query: store, access policy, and principal required")
	}
	return &Tool{store: repository, access: access, principal: principal}, nil
}

func (t *Tool) Name() string { return "research_query" }

func (t *Tool) Description() string {
	return "Read bounded, durable metadata for an authorized research campaign by object ID or list campaign-owned objects. Returns evidence references, never raw artifact bytes or host storage paths."
}

func (t *Tool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["operation","object_type"],
  "properties":{
    "operation":{"type":"string","enum":["get","list"]},
    "object_type":{"type":"string","enum":["campaign","target","build","run","crash_observation","crash_group","primitive_assessment","finding","artifact","approval","source_evidence","source_review","revision_check","remediation","apparatus"]},
    "id":{"type":"string","minLength":1,"maxLength":256},
    "campaign_id":{"type":"string","minLength":1,"maxLength":256},
    "limit":{"type":"integer","minimum":1,"maximum":20}
  }
}`)
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeArguments(raw)
	if err != nil {
		return "", err
	}
	var value any
	switch args.Operation {
	case "get":
		if args.ID == "" || args.CampaignID != "" || args.Limit != 0 {
			return "", errors.New("research query: get requires id and does not accept campaign_id or limit")
		}
		value, err = t.get(ctx, args.ObjectType, args.ID)
	case "list":
		if args.ID != "" {
			return "", errors.New("research query: list does not accept id")
		}
		if args.Limit == 0 {
			args.Limit = defaultListLimit
		}
		if args.Limit < 1 || args.Limit > maxListLimit {
			return "", fmt.Errorf("research query: limit must be between 1 and %d", maxListLimit)
		}
		value, err = t.list(ctx, args.ObjectType, args.CampaignID, args.Limit)
	default:
		return "", errors.New("research query: operation must be get or list")
	}
	if err != nil {
		return "", err
	}
	response, err := json.Marshal(map[string]any{"object": args.ObjectType, "operation": args.Operation, "data": value})
	if err != nil {
		return "", err
	}
	if len(response) > maxQueryOutputBytes {
		return "", fmt.Errorf("research query: bounded metadata response exceeds %d bytes; request a smaller list or a single object", maxQueryOutputBytes)
	}
	return string(response), nil
}

func decodeArguments(raw json.RawMessage) (arguments, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args arguments
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("research query: invalid arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return args, errors.New("research query: trailing JSON data")
	}
	args.Operation = strings.TrimSpace(args.Operation)
	args.ObjectType = strings.TrimSpace(args.ObjectType)
	args.ID = strings.TrimSpace(args.ID)
	args.CampaignID = strings.TrimSpace(args.CampaignID)
	return args, nil
}

func (t *Tool) get(ctx context.Context, objectType, id string) (any, error) {
	var value any
	var campaignID string
	var err error
	switch objectType {
	case "campaign":
		var object domain.Campaign
		object, err = t.store.GetCampaign(ctx, id)
		campaignID, value = object.ID, object
	case "target":
		var object domain.TargetRevision
		object, err = t.store.GetTarget(ctx, id)
		campaignID, value = object.CampaignID, object
	case "build":
		var object domain.Build
		object, err = t.store.GetBuild(ctx, id)
		campaignID, value = object.CampaignID, object
	case "run":
		var object domain.ExperimentRun
		object, err = t.store.GetRun(ctx, id)
		campaignID, value = object.CampaignID, object
	case "crash_observation":
		var object domain.CrashObservation
		object, err = t.store.GetCrash(ctx, id)
		campaignID, value = object.CampaignID, object
	case "crash_group":
		var object domain.CrashGroup
		object, err = t.store.GetCrashGroup(ctx, id)
		campaignID, value = object.CampaignID, object
	case "primitive_assessment":
		var object domain.PrimitiveAssessment
		object, err = t.store.GetPrimitive(ctx, id)
		campaignID, value = object.CampaignID, object
	case "finding":
		var object domain.Finding
		object, err = t.store.GetFinding(ctx, id)
		campaignID, value = object.CampaignID, object
	case "artifact":
		var object domain.Artifact
		object, err = t.store.GetArtifact(ctx, id)
		campaignID = object.CampaignID
		if err == nil {
			if object.Sensitivity == "private_disclosure" && !hasAnyRole(t.principal, domain.RoleOperator, domain.RoleReviewer) {
				return nil, errors.New("research query: private disclosure metadata requires operator or reviewer role")
			}
			object.StoragePath = ""
		}
		value = object
	case "approval":
		var object domain.Approval
		object, err = t.store.GetApproval(ctx, id)
		campaignID, value = object.CampaignID, object
	case "source_evidence":
		var object domain.SourceEvidence
		object, err = t.store.GetSourceEvidence(ctx, id)
		campaignID, value = object.CampaignID, object
	case "source_review":
		var object domain.SourceReview
		object, err = t.store.GetSourceReview(ctx, id)
		campaignID, value = object.CampaignID, object
	case "revision_check":
		var object domain.RevisionCheck
		object, err = t.store.GetRevisionCheck(ctx, id)
		campaignID, value = object.CampaignID, object
	case "remediation":
		var object domain.RemediationValidation
		object, err = t.store.GetRemediation(ctx, id)
		campaignID, value = object.CampaignID, object
	case "apparatus":
		return t.store.GetApparatus(ctx, id)
	default:
		return nil, errors.New("research query: unsupported object_type")
	}
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCampaign(ctx, campaignID); err != nil {
		return nil, err
	}
	return value, nil
}

func (t *Tool) list(ctx context.Context, objectType, campaignID string, limit int) (any, error) {
	if objectType == "apparatus" {
		if campaignID != "" {
			return nil, errors.New("research query: apparatus list does not accept campaign_id")
		}
		return t.store.ListApparatus(ctx, limit)
	}
	if objectType == "campaign" {
		if campaignID != "" {
			return nil, errors.New("research query: campaign list does not accept campaign_id")
		}
		// Scan the store's bounded campaign page before applying membership so a
		// run of newer unauthorized campaigns cannot hide older authorized ones.
		campaigns, err := t.store.ListCampaigns(ctx, 1000)
		if err != nil {
			return nil, err
		}
		result := make([]domain.Campaign, 0, min(limit, len(campaigns)))
		for _, campaign := range campaigns {
			allowed, accessErr := t.access.CanReadCampaign(ctx, t.principal, campaign.ID)
			if accessErr != nil {
				continue
			}
			if allowed {
				result = append(result, campaign)
				if len(result) == limit {
					break
				}
			}
		}
		return result, nil
	}
	if campaignID == "" {
		return nil, errors.New("research query: campaign_id is required for this list")
	}
	if err := t.authorizeCampaign(ctx, campaignID); err != nil {
		return nil, err
	}
	switch objectType {
	case "target":
		return t.store.ListTargets(ctx, campaignID, limit)
	case "build":
		return t.store.ListBuilds(ctx, campaignID, limit)
	case "run":
		return t.store.ListRuns(ctx, campaignID, limit)
	case "crash_observation":
		return t.store.ListCrashes(ctx, campaignID, limit)
	case "crash_group":
		return t.store.ListCrashGroups(ctx, campaignID, limit)
	case "primitive_assessment":
		return t.store.ListPrimitives(ctx, campaignID, limit)
	case "finding":
		return t.store.ListFindings(ctx, campaignID, limit)
	case "artifact":
		artifacts, err := t.store.ListArtifacts(ctx, campaignID, limit)
		if err != nil {
			return nil, err
		}
		filtered := artifacts[:0]
		for _, artifact := range artifacts {
			if artifact.Sensitivity == "private_disclosure" && !hasAnyRole(t.principal, domain.RoleOperator, domain.RoleReviewer) {
				continue
			}
			artifact.StoragePath = ""
			filtered = append(filtered, artifact)
		}
		return filtered, nil
	case "approval":
		return t.store.ListApprovals(ctx, campaignID, limit)
	case "source_evidence":
		return t.store.ListSourceEvidence(ctx, campaignID, limit)
	case "source_review":
		return t.store.ListSourceReviews(ctx, campaignID, limit)
	case "revision_check":
		return t.store.ListRevisionChecks(ctx, campaignID, limit)
	case "remediation":
		return t.store.ListRemediations(ctx, campaignID, limit)
	default:
		return nil, errors.New("research query: unsupported object_type")
	}
}

func (t *Tool) authorizeCampaign(ctx context.Context, campaignID string) error {
	if campaignID == "" {
		return errors.New("research query: object has no campaign identity")
	}
	allowed, err := t.access.CanReadCampaign(ctx, t.principal, campaignID)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("research query: principal is not a campaign member")
	}
	return nil
}

func hasAnyRole(principal domain.Principal, roles ...domain.Role) bool {
	for _, actual := range principal.Roles {
		if actual == domain.RoleAdmin {
			return true
		}
		for _, wanted := range roles {
			if actual == wanted {
				return true
			}
		}
	}
	return false
}

var _ tools.Tool = (*Tool)(nil)
