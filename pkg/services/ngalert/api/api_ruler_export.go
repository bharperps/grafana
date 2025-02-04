package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/grafana/grafana/pkg/api/response"
	contextmodel "github.com/grafana/grafana/pkg/services/contexthandler/model"
	apimodels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	ngmodels "github.com/grafana/grafana/pkg/services/ngalert/models"
)

// ExportFromPayload converts the rule groups from the argument `ruleGroupConfig` to export format. All rules are expected to be fully specified. The access to data sources mentioned in the rules is not enforced.
// Can return 403 StatusForbidden if user is not authorized to read folder `namespaceTitle`
func (srv RulerSrv) ExportFromPayload(c *contextmodel.ReqContext, ruleGroupConfig apimodels.PostableRuleGroupConfig, namespaceTitle string) response.Response {
	namespace, err := srv.store.GetNamespaceByTitle(c.Req.Context(), namespaceTitle, c.SignedInUser.OrgID, c.SignedInUser)
	if err != nil {
		return toNamespaceErrorResponse(err)
	}

	rulesWithOptionals, err := validateRuleGroup(&ruleGroupConfig, c.SignedInUser.OrgID, namespace, srv.cfg)
	if err != nil {
		return ErrResp(http.StatusBadRequest, err, "")
	}

	if len(rulesWithOptionals) == 0 {
		return ErrResp(http.StatusBadRequest, err, "")
	}

	rules := make([]ngmodels.AlertRule, 0, len(rulesWithOptionals))
	for _, optional := range rulesWithOptionals {
		rules = append(rules, optional.AlertRule)
	}

	groupsWithTitle := ngmodels.NewAlertRuleGroupWithFolderTitle(rules[0].GetGroupKey(), rules, namespace.Title)

	e, err := AlertingFileExportFromAlertRuleGroupWithFolderTitle([]ngmodels.AlertRuleGroupWithFolderTitle{groupsWithTitle})
	if err != nil {
		return ErrResp(http.StatusInternalServerError, err, "failed to create alerting file export")
	}

	return exportResponse(c, e)
}

// ExportRules reads alert rules that user has access to from database according to the filters.
func (srv RulerSrv) ExportRules(c *contextmodel.ReqContext) response.Response {
	// The similar method exists in provisioning (see ProvisioningSrv.RouteGetAlertRulesExport).
	// Modification to parameters and response format should be made in these two methods at the same time.

	folderUIDs := c.QueryStrings("folderUid")
	group := c.Query("group")
	uid := c.Query("ruleUid")

	var groups []ngmodels.AlertRuleGroupWithFolderTitle
	if uid != "" {
		if group != "" || len(folderUIDs) > 0 {
			return ErrResp(http.StatusBadRequest, errors.New("group and folder should not be specified when a single rule is requested"), "")
		}
		rulesGroup, err := srv.getRuleWithFolderTitleByRuleUid(c, uid)
		if err != nil {
			return errorToResponse(err)
		}
		groups = []ngmodels.AlertRuleGroupWithFolderTitle{rulesGroup}
	} else if group != "" {
		if len(folderUIDs) != 1 || folderUIDs[0] == "" {
			return ErrResp(http.StatusBadRequest,
				fmt.Errorf("group name must be specified together with a single folder_uid parameter. Got %d", len(folderUIDs)),
				"",
			)
		}
		rulesGroup, err := srv.getRuleGroupWithFolderTitle(c, ngmodels.AlertRuleGroupKey{
			OrgID:        c.OrgID,
			NamespaceUID: folderUIDs[0],
			RuleGroup:    group,
		})
		if err != nil {
			return errorToResponse(err)
		}
		groups = []ngmodels.AlertRuleGroupWithFolderTitle{rulesGroup}
	} else {
		var err error
		groups, err = srv.getRulesWithFolderTitleInFolders(c, folderUIDs)
		if err != nil {
			return errorToResponse(err)
		}
	}

	if len(groups) == 0 {
		return response.Empty(http.StatusNotFound)
	}

	// sort result so the response is always stable
	ngmodels.SortAlertRuleGroupWithFolderTitle(groups)

	e, err := AlertingFileExportFromAlertRuleGroupWithFolderTitle(groups)
	if err != nil {
		return ErrResp(http.StatusInternalServerError, err, "failed to create alerting file export")
	}
	return exportResponse(c, e)
}

// getRuleWithFolderTitleByRuleUid calls getAuthorizedRuleByUid and combines its result with folder (aka namespace) title.
func (srv RulerSrv) getRuleWithFolderTitleByRuleUid(c *contextmodel.ReqContext, ruleUID string) (ngmodels.AlertRuleGroupWithFolderTitle, error) {
	rule, err := srv.getAuthorizedRuleByUid(c.Req.Context(), c, ruleUID)
	if err != nil {
		return ngmodels.AlertRuleGroupWithFolderTitle{}, err
	}
	namespace, err := srv.store.GetNamespaceByUID(c.Req.Context(), rule.NamespaceUID, c.SignedInUser.OrgID, c.SignedInUser)
	if err != nil {
		return ngmodels.AlertRuleGroupWithFolderTitle{}, errors.Join(errFolderAccess, err)
	}
	return ngmodels.NewAlertRuleGroupWithFolderTitle(rule.GetGroupKey(), []ngmodels.AlertRule{rule}, namespace.Title), nil
}

// getRuleGroupWithFolderTitle calls getAuthorizedRuleGroup and combines its result with folder (aka namespace) title.
func (srv RulerSrv) getRuleGroupWithFolderTitle(c *contextmodel.ReqContext, ruleGroupKey ngmodels.AlertRuleGroupKey) (ngmodels.AlertRuleGroupWithFolderTitle, error) {
	namespace, err := srv.store.GetNamespaceByUID(c.Req.Context(), ruleGroupKey.NamespaceUID, c.SignedInUser.OrgID, c.SignedInUser)
	if err != nil {
		return ngmodels.AlertRuleGroupWithFolderTitle{}, errors.Join(errFolderAccess, err)
	}
	rules, err := srv.getAuthorizedRuleGroup(c.Req.Context(), c, ruleGroupKey)
	if err != nil {
		return ngmodels.AlertRuleGroupWithFolderTitle{}, err
	}
	if len(rules) == 0 {
		return ngmodels.AlertRuleGroupWithFolderTitle{}, ngmodels.ErrAlertRuleNotFound
	}
	return ngmodels.NewAlertRuleGroupWithFolderTitleFromRulesGroup(ruleGroupKey, rules, namespace.Title), nil
}

// getRulesWithFolderTitleInFolders gets list of folders to which user has access, and then calls searchAuthorizedAlertRules.
// If argument folderUIDs is not empty it intersects it with the list of folders available for user and then retrieves rules that are in those folders.
func (srv RulerSrv) getRulesWithFolderTitleInFolders(c *contextmodel.ReqContext, folderUIDs []string) ([]ngmodels.AlertRuleGroupWithFolderTitle, error) {
	folders, err := srv.store.GetUserVisibleNamespaces(c.Req.Context(), c.OrgID, c.SignedInUser)
	if err != nil {
		return nil, err
	}
	query := ngmodels.ListAlertRulesQuery{
		OrgID:         c.OrgID,
		NamespaceUIDs: nil,
	}
	if len(folderUIDs) > 0 {
		for _, folderUID := range folderUIDs {
			if _, ok := folders[folderUID]; ok {
				query.NamespaceUIDs = append(query.NamespaceUIDs, folderUID)
			}
		}
		if len(query.NamespaceUIDs) == 0 {
			return nil, fmt.Errorf("%w access rules in the specified folders", ErrAuthorization)
		}
	} else {
		for _, folder := range folders {
			query.NamespaceUIDs = append(query.NamespaceUIDs, folder.UID)
		}
	}

	rulesByGroup, _, err := srv.searchAuthorizedAlertRules(c.Req.Context(), c, folderUIDs, "", 0)
	if err != nil {
		return nil, err
	}

	result := make([]ngmodels.AlertRuleGroupWithFolderTitle, 0, len(rulesByGroup))
	for groupKey, rulesGroup := range rulesByGroup {
		namespace, ok := folders[groupKey.NamespaceUID]
		if !ok {
			continue // user does not have access
		}
		result = append(result, ngmodels.NewAlertRuleGroupWithFolderTitleFromRulesGroup(groupKey, rulesGroup, namespace.Title))
	}
	return result, nil
}
