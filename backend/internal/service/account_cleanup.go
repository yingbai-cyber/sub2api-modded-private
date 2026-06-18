package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	AccountCleanupActionDelete AccountCleanupAction = "delete"
	AccountCleanupActionMove   AccountCleanupAction = "move"

	accountCleanupDefaultLimit    = 1000
	accountCleanupMaxLimit        = 5000
	accountCleanupDefaultPageSize = 50
	accountCleanupMaxPageSize     = 200
	accountCleanupFetchPageSize   = 500
)

// AccountCleanupService is a narrow local extension for the modded account cleanup workflow.
// It is intentionally kept out of AdminService to reduce upstream rebase churn.
type AccountCleanupService interface {
	PreviewAccountCleanup(ctx context.Context, input *AccountCleanupInput) (*AccountCleanupPreviewResult, error)
	ExecuteAccountCleanup(ctx context.Context, input *AccountCleanupInput) (*AccountCleanupExecuteResult, error)
}

type AccountCleanupAction string

type AccountCleanupInput struct {
	SourceGroupID int64                `json:"source_group_id"`
	Statuses      []string             `json:"statuses"`
	Action        AccountCleanupAction `json:"action"`
	TargetGroupID *int64               `json:"target_group_id,omitempty"`
	Platform      string               `json:"platform,omitempty"`
	Type          string               `json:"type,omitempty"`
	Search        string               `json:"search,omitempty"`
	Page          int                  `json:"page,omitempty"`
	PageSize      int                  `json:"page_size,omitempty"`
	Limit         int                  `json:"limit,omitempty"`
	AccountIDs    []int64              `json:"account_ids,omitempty"`
	ConfirmText   string               `json:"confirm_text,omitempty"`
}

type AccountCleanupPreviewGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type AccountCleanupPreviewItem struct {
	ID            int64                        `json:"id"`
	Name          string                       `json:"name"`
	Platform      string                       `json:"platform"`
	Type          string                       `json:"type"`
	Status        string                       `json:"status"`
	Schedulable   bool                         `json:"schedulable"`
	ErrorMessage  string                       `json:"error_message,omitempty"`
	GroupIDs      []int64                      `json:"group_ids,omitempty"`
	Groups        []AccountCleanupPreviewGroup `json:"groups,omitempty"`
	LastUsedAt    *time.Time                   `json:"last_used_at,omitempty"`
	Reason        string                       `json:"reason"`
	TargetGroupID *int64                       `json:"target_group_id,omitempty"`
}

type AccountCleanupSummary struct {
	ByStatus   map[string]int `json:"by_status"`
	ByPlatform map[string]int `json:"by_platform"`
}

type AccountCleanupPreviewResult struct {
	Action        AccountCleanupAction        `json:"action"`
	SourceGroupID int64                       `json:"source_group_id"`
	TargetGroupID *int64                      `json:"target_group_id,omitempty"`
	Matched       int                         `json:"matched"`
	Page          int                         `json:"page"`
	PageSize      int                         `json:"page_size"`
	Pages         int                         `json:"pages"`
	Limit         int                         `json:"limit"`
	Capped        bool                        `json:"capped"`
	Items         []AccountCleanupPreviewItem `json:"items"`
	Summary       AccountCleanupSummary       `json:"summary"`
}

type AccountCleanupFailedItem struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name,omitempty"`
	Error     string `json:"error"`
}

type AccountCleanupSkippedItem struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name,omitempty"`
	Reason    string `json:"reason"`
}

type AccountCleanupExecuteResult struct {
	Total        int                         `json:"total"`
	Success      int                         `json:"success"`
	Failed       int                         `json:"failed"`
	Skipped      int                         `json:"skipped"`
	SuccessIDs   []int64                     `json:"success_ids"`
	FailedItems  []AccountCleanupFailedItem  `json:"failed_items"`
	SkippedItems []AccountCleanupSkippedItem `json:"skipped_items"`
}

type accountCleanupResolvedInput struct {
	input         AccountCleanupInput
	queryStatuses []string
	matchStatuses map[string]struct{}
}

func (s *adminServiceImpl) PreviewAccountCleanup(ctx context.Context, input *AccountCleanupInput) (*AccountCleanupPreviewResult, error) {
	resolved, err := s.resolveAccountCleanupInput(ctx, input, false)
	if err != nil {
		return nil, err
	}

	accounts, capped, _, err := s.collectAccountCleanupCandidates(ctx, resolved)
	if err != nil {
		return nil, err
	}

	items := accountCleanupPreviewItems(accounts, resolved.input.Action, resolved.input.TargetGroupID)
	summary := buildAccountCleanupSummary(accounts)
	page := resolved.input.Page
	pageSize := resolved.input.PageSize
	pages := accountCleanupPages(len(items), pageSize)
	start, end := accountCleanupPageBounds(len(items), page, pageSize)
	paged := []AccountCleanupPreviewItem{}
	if start < end {
		paged = items[start:end]
	}

	return &AccountCleanupPreviewResult{
		Action:        resolved.input.Action,
		SourceGroupID: resolved.input.SourceGroupID,
		TargetGroupID: resolved.input.TargetGroupID,
		Matched:       len(items),
		Page:          page,
		PageSize:      pageSize,
		Pages:         pages,
		Limit:         resolved.input.Limit,
		Capped:        capped,
		Items:         paged,
		Summary:       summary,
	}, nil
}

func (s *adminServiceImpl) ExecuteAccountCleanup(ctx context.Context, input *AccountCleanupInput) (*AccountCleanupExecuteResult, error) {
	resolved, err := s.resolveAccountCleanupInput(ctx, input, true)
	if err != nil {
		return nil, err
	}

	accounts, _, skipped, err := s.collectAccountCleanupCandidates(ctx, resolved)
	if err != nil {
		return nil, err
	}

	result := &AccountCleanupExecuteResult{
		Total:        len(accounts),
		SuccessIDs:   make([]int64, 0, len(accounts)),
		FailedItems:  make([]AccountCleanupFailedItem, 0),
		SkippedItems: skipped,
	}
	result.Skipped = len(result.SkippedItems)

	for _, account := range accounts {
		switch resolved.input.Action {
		case AccountCleanupActionDelete:
			if err := s.accountRepo.Delete(ctx, account.ID); err != nil {
				result.Failed++
				result.FailedItems = append(result.FailedItems, AccountCleanupFailedItem{AccountID: account.ID, Name: account.Name, Error: err.Error()})
				continue
			}
		case AccountCleanupActionMove:
			targetID := int64(0)
			if resolved.input.TargetGroupID != nil {
				targetID = *resolved.input.TargetGroupID
			}
			if err := s.checkMixedChannelRisk(ctx, account.ID, account.Platform, []int64{targetID}); err != nil {
				result.Failed++
				result.FailedItems = append(result.FailedItems, AccountCleanupFailedItem{AccountID: account.ID, Name: account.Name, Error: err.Error()})
				continue
			}
			if err := s.accountRepo.BindGroups(ctx, account.ID, []int64{targetID}); err != nil {
				result.Failed++
				result.FailedItems = append(result.FailedItems, AccountCleanupFailedItem{AccountID: account.ID, Name: account.Name, Error: err.Error()})
				continue
			}
		default:
			return nil, accountCleanupBadRequest("ACCOUNT_CLEANUP_INVALID_ACTION", "invalid cleanup action")
		}

		result.Success++
		result.SuccessIDs = append(result.SuccessIDs, account.ID)
	}

	return result, nil
}

func (s *adminServiceImpl) resolveAccountCleanupInput(ctx context.Context, input *AccountCleanupInput, execute bool) (accountCleanupResolvedInput, error) {
	if input == nil {
		return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_INVALID_REQUEST", "request body is required")
	}
	resolved := accountCleanupResolvedInput{input: *input}
	resolved.input.Platform = strings.TrimSpace(resolved.input.Platform)
	resolved.input.Type = strings.TrimSpace(resolved.input.Type)
	resolved.input.Search = strings.TrimSpace(resolved.input.Search)
	resolved.input.ConfirmText = strings.TrimSpace(resolved.input.ConfirmText)

	if resolved.input.SourceGroupID <= 0 {
		return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_SOURCE_GROUP_REQUIRED", "source_group_id is required")
	}
	if len(resolved.input.Search) > 100 {
		resolved.input.Search = resolved.input.Search[:100]
	}
	if resolved.input.Page <= 0 {
		resolved.input.Page = 1
	}
	if resolved.input.PageSize <= 0 {
		resolved.input.PageSize = accountCleanupDefaultPageSize
	}
	if resolved.input.PageSize > accountCleanupMaxPageSize {
		resolved.input.PageSize = accountCleanupMaxPageSize
	}
	if resolved.input.Limit <= 0 {
		resolved.input.Limit = accountCleanupDefaultLimit
	}
	if resolved.input.Limit > accountCleanupMaxLimit {
		resolved.input.Limit = accountCleanupMaxLimit
	}

	queryStatuses, matchStatuses, err := resolveAccountCleanupStatuses(resolved.input.Statuses)
	if err != nil {
		return accountCleanupResolvedInput{}, err
	}
	resolved.queryStatuses = queryStatuses
	resolved.matchStatuses = matchStatuses

	switch resolved.input.Action {
	case AccountCleanupActionDelete:
		if execute && resolved.input.ConfirmText != "DELETE" {
			return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_DELETE_CONFIRM_REQUIRED", "type DELETE to confirm account deletion")
		}
	case AccountCleanupActionMove:
		if resolved.input.TargetGroupID == nil || *resolved.input.TargetGroupID <= 0 {
			return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_TARGET_GROUP_REQUIRED", "target_group_id is required when action is move")
		}
		if *resolved.input.TargetGroupID == resolved.input.SourceGroupID {
			return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_SAME_GROUP", "target group must be different from source group")
		}
	default:
		return accountCleanupResolvedInput{}, accountCleanupBadRequest("ACCOUNT_CLEANUP_INVALID_ACTION", "action must be delete or move")
	}

	if err := s.validateGroupIDsExist(ctx, []int64{resolved.input.SourceGroupID}); err != nil {
		return accountCleanupResolvedInput{}, err
	}
	if resolved.input.Action == AccountCleanupActionMove && resolved.input.TargetGroupID != nil {
		if err := s.validateGroupIDsExist(ctx, []int64{*resolved.input.TargetGroupID}); err != nil {
			return accountCleanupResolvedInput{}, err
		}
	}

	return resolved, nil
}

func resolveAccountCleanupStatuses(statuses []string) ([]string, map[string]struct{}, error) {
	querySeen := map[string]struct{}{}
	matchSeen := map[string]struct{}{}
	queryStatuses := make([]string, 0, len(statuses))

	for _, raw := range statuses {
		status := strings.ToLower(strings.TrimSpace(raw))
		if status == "" {
			continue
		}
		switch status {
		case StatusActive, StatusError, "rate_limited", "temp_unschedulable", "unschedulable":
			if _, ok := querySeen[status]; !ok {
				querySeen[status] = struct{}{}
				queryStatuses = append(queryStatuses, status)
			}
			matchSeen[status] = struct{}{}
		case "inactive", StatusDisabled:
			for _, queryStatus := range []string{"inactive", StatusDisabled} {
				if _, ok := querySeen[queryStatus]; !ok {
					querySeen[queryStatus] = struct{}{}
					queryStatuses = append(queryStatuses, queryStatus)
				}
				matchSeen[queryStatus] = struct{}{}
			}
		default:
			return nil, nil, accountCleanupBadRequest("ACCOUNT_CLEANUP_INVALID_STATUS", fmt.Sprintf("unsupported account status: %s", status))
		}
	}

	if len(queryStatuses) == 0 {
		return nil, nil, accountCleanupBadRequest("ACCOUNT_CLEANUP_STATUS_REQUIRED", "at least one account status is required")
	}
	return queryStatuses, matchSeen, nil
}

func (s *adminServiceImpl) collectAccountCleanupCandidates(ctx context.Context, resolved accountCleanupResolvedInput) ([]Account, bool, []AccountCleanupSkippedItem, error) {
	if len(resolved.input.AccountIDs) > 0 {
		return s.collectAccountCleanupCandidatesByIDs(ctx, resolved)
	}
	accounts, capped, err := s.collectAccountCleanupCandidatesByFilters(ctx, resolved)
	return accounts, capped, nil, err
}

func (s *adminServiceImpl) collectAccountCleanupCandidatesByIDs(ctx context.Context, resolved accountCleanupResolvedInput) ([]Account, bool, []AccountCleanupSkippedItem, error) {
	uniqueIDs := uniquePositiveInt64s(resolved.input.AccountIDs)
	if len(uniqueIDs) == 0 {
		return []Account{}, false, nil, nil
	}

	accounts, err := s.accountRepo.GetByIDs(ctx, uniqueIDs)
	if err != nil {
		return nil, false, nil, err
	}

	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}

	out := make([]Account, 0, len(uniqueIDs))
	skipped := make([]AccountCleanupSkippedItem, 0)
	for _, id := range uniqueIDs {
		account := byID[id]
		if account == nil {
			skipped = append(skipped, AccountCleanupSkippedItem{AccountID: id, Reason: "account_not_found"})
			continue
		}
		if !accountMatchesCleanupInput(*account, resolved) {
			skipped = append(skipped, AccountCleanupSkippedItem{AccountID: id, Name: account.Name, Reason: "account_no_longer_matches_filters"})
			continue
		}
		out = append(out, *account)
		if len(out) >= resolved.input.Limit {
			sortAccountsByID(out)
			return out, true, skipped, nil
		}
	}
	sortAccountsByID(out)
	return out, false, skipped, nil
}

func (s *adminServiceImpl) collectAccountCleanupCandidatesByFilters(ctx context.Context, resolved accountCleanupResolvedInput) ([]Account, bool, error) {
	seen := make(map[int64]struct{})
	out := make([]Account, 0)

	for _, status := range resolved.queryStatuses {
		page := 1
		for {
			accounts, result, err := s.accountRepo.ListWithFilters(
				ctx,
				pagination.PaginationParams{Page: page, PageSize: accountCleanupFetchPageSize, SortBy: "id", SortOrder: pagination.SortOrderAsc},
				resolved.input.Platform,
				resolved.input.Type,
				status,
				resolved.input.Search,
				resolved.input.SourceGroupID,
				"",
			)
			if err != nil {
				return nil, false, err
			}
			for _, account := range accounts {
				if _, ok := seen[account.ID]; ok {
					continue
				}
				if !accountMatchesCleanupInput(account, resolved) {
					continue
				}
				seen[account.ID] = struct{}{}
				out = append(out, account)
				if len(out) >= resolved.input.Limit {
					sortAccountsByID(out)
					return out, true, nil
				}
			}
			if result == nil || len(accounts) == 0 || page >= result.Pages {
				break
			}
			page++
		}
	}

	sortAccountsByID(out)
	return out, false, nil
}

func accountMatchesCleanupInput(account Account, resolved accountCleanupResolvedInput) bool {
	if resolved.input.Platform != "" && account.Platform != resolved.input.Platform {
		return false
	}
	if resolved.input.Type != "" && account.Type != resolved.input.Type {
		return false
	}
	if resolved.input.Search != "" && !strings.Contains(strings.ToLower(account.Name), strings.ToLower(resolved.input.Search)) {
		return false
	}
	if !accountHasGroup(account, resolved.input.SourceGroupID) {
		return false
	}
	return accountMatchesCleanupStatus(account, resolved.matchStatuses)
}

func accountMatchesCleanupStatus(account Account, statuses map[string]struct{}) bool {
	now := time.Now()
	if account.Status != StatusActive {
		if _, ok := statuses[account.Status]; ok {
			return true
		}
	}
	if account.Status == "inactive" {
		if _, ok := statuses[StatusDisabled]; ok {
			return true
		}
	}
	if account.Status == StatusDisabled {
		if _, ok := statuses["inactive"]; ok {
			return true
		}
	}
	if _, ok := statuses[StatusActive]; ok {
		if account.Status == StatusActive && account.Schedulable && !accountRateLimitedAt(now, account) && !accountTempUnschedulableAt(now, account) {
			return true
		}
	}
	if _, ok := statuses["rate_limited"]; ok {
		if account.Status == StatusActive && accountRateLimitedAt(now, account) && !accountTempUnschedulableAt(now, account) {
			return true
		}
	}
	if _, ok := statuses["temp_unschedulable"]; ok {
		if account.Status == StatusActive && accountTempUnschedulableAt(now, account) {
			return true
		}
	}
	if _, ok := statuses["unschedulable"]; ok {
		if account.Status == StatusActive && !account.Schedulable && !accountRateLimitedAt(now, account) && !accountTempUnschedulableAt(now, account) {
			return true
		}
	}
	return false
}

func accountRateLimitedAt(now time.Time, account Account) bool {
	return account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt)
}

func accountTempUnschedulableAt(now time.Time, account Account) bool {
	return account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil)
}

func accountHasGroup(account Account, groupID int64) bool {
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, group := range account.Groups {
		if group != nil && group.ID == groupID {
			return true
		}
	}
	return false
}

func accountCleanupPreviewItems(accounts []Account, action AccountCleanupAction, targetGroupID *int64) []AccountCleanupPreviewItem {
	items := make([]AccountCleanupPreviewItem, 0, len(accounts))
	for _, account := range accounts {
		groups := make([]AccountCleanupPreviewGroup, 0, len(account.Groups))
		for _, group := range account.Groups {
			if group == nil {
				continue
			}
			groups = append(groups, AccountCleanupPreviewGroup{ID: group.ID, Name: group.Name})
		}
		items = append(items, AccountCleanupPreviewItem{
			ID:            account.ID,
			Name:          account.Name,
			Platform:      account.Platform,
			Type:          account.Type,
			Status:        account.Status,
			Schedulable:   account.Schedulable,
			ErrorMessage:  account.ErrorMessage,
			GroupIDs:      append([]int64(nil), account.GroupIDs...),
			Groups:        groups,
			LastUsedAt:    account.LastUsedAt,
			Reason:        accountCleanupReason(account),
			TargetGroupID: targetGroupID,
		})
	}
	return items
}

func accountCleanupReason(account Account) string {
	now := time.Now()
	if account.Status != "" && account.Status != StatusActive {
		return "status:" + account.Status
	}
	if account.Status == StatusActive && !account.Schedulable && !accountRateLimitedAt(now, account) && !accountTempUnschedulableAt(now, account) {
		return "unschedulable"
	}
	if accountRateLimitedAt(now, account) {
		return "rate_limited"
	}
	if accountTempUnschedulableAt(now, account) {
		return "temp_unschedulable"
	}
	return "status:" + account.Status
}

func buildAccountCleanupSummary(accounts []Account) AccountCleanupSummary {
	summary := AccountCleanupSummary{
		ByStatus:   make(map[string]int),
		ByPlatform: make(map[string]int),
	}
	for _, account := range accounts {
		status := account.Status
		if status == "" {
			status = "unknown"
		}
		platform := account.Platform
		if platform == "" {
			platform = "unknown"
		}
		summary.ByStatus[status]++
		summary.ByPlatform[platform]++
	}
	return summary
}

func accountCleanupPageBounds(total, page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = accountCleanupDefaultPageSize
	}
	start := (page - 1) * pageSize
	if start >= total {
		return total, total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return start, end
}

func accountCleanupPages(total, pageSize int) int {
	if pageSize <= 0 {
		pageSize = accountCleanupDefaultPageSize
	}
	if total == 0 {
		return 0
	}
	return (total + pageSize - 1) / pageSize
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sortAccountsByID(accounts []Account) {
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].ID < accounts[j].ID
	})
}

func accountCleanupBadRequest(reason, message string) error {
	return infraerrors.BadRequest(reason, message)
}
