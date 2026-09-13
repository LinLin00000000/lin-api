package service

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// IdentitySnapshotProvider is a constructor dependency, never a header, query or
// user-controlled Gin value. The production provider is the activation-gated store.
type IdentitySnapshotProvider func() *identityservice.Snapshot

type identityRequestKey struct{}

// IdentityRequest owns admission-time authorization inputs. It is deliberately
// not a price/settlement snapshot. B3 must freeze the complete billing contract
// against Snapshot(), before upstream I/O, and persist that contract for tasks.
type IdentityRequest struct {
	snapshot         *identityservice.Snapshot
	userID           int
	identity         string
	tokenGroup       string
	services         map[string]string
	autoGroups       []string
	whitelistEnabled bool
	whitelist        map[string]bool
}

// FreezeIdentityRequest captures authenticated account and Key inputs exactly
// once. Legacy continues the original paths; no runtime activation is granted.
func FreezeIdentityRequest(c *gin.Context, s *identityservice.Snapshot, userID int, identity string) *IdentityRequest {
	if existing := RequestIdentity(c); existing != nil {
		return existing
	}
	if s == nil {
		return nil
	}
	cfg := s.Config()
	if cfg.Mode != identityservice.ModeIdentityService {
		return nil
	}
	r := &IdentityRequest{snapshot: s, userID: userID, identity: identity, services: map[string]string{}, whitelist: map[string]bool{}}
	r.tokenGroup = common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
	if r.tokenGroup == "" {
		r.tokenGroup = identity
	}
	// No implicit identity-as-service membership in the new mode.
	usable := getUserUsableGroups(identity, false)
	for g, desc := range usable {
		if _, ok := cfg.ServiceDefaults[g]; ok && g != "auto" {
			r.services[g] = desc
		}
	}
	r.whitelistEnabled = common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled)
	if value, ok := common.GetContextKey(c, constant.ContextKeyTokenModelLimit); ok {
		if limits, ok := value.(map[string]bool); ok {
			for m, allow := range limits {
				r.whitelist[m] = allow
			}
		}
	}
	groups := setting.GetAutoGroups()
	limit := len(groups)
	if value, exists := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups); exists {
		groups, _ = value.([]string)
		limit = setting.GetMaxTokenAutoGroups()
	}
	if _, allowed := usable["auto"]; !allowed {
		groups = nil
	}
	seen := map[string]bool{}
	for _, g := range groups {
		if r.ServiceAllowed(g) && !seen[g] && len(r.autoGroups) < limit {
			r.autoGroups = append(r.autoGroups, g)
			seen[g] = true
		}
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), identityRequestKey{}, r))
	return r
}

func RequestIdentity(c *gin.Context) *IdentityRequest {
	if c == nil || c.Request == nil {
		return nil
	}
	r, _ := c.Request.Context().Value(identityRequestKey{}).(*IdentityRequest)
	return r
}
func (r *IdentityRequest) Identity() string   { return r.identity }
func (r *IdentityRequest) UserID() int        { return r.userID }
func (r *IdentityRequest) TokenGroup() string { return r.tokenGroup }
func (r *IdentityRequest) Groups() []string {
	if r.tokenGroup == "auto" {
		return r.AutoGroups()
	}
	return []string{r.tokenGroup}
}
func (r *IdentityRequest) Snapshot() *identityservice.Snapshot { return r.snapshot }
func (r *IdentityRequest) ServiceAllowed(g string) bool        { _, ok := r.services[g]; return ok }
func (r *IdentityRequest) AutoGroups() []string                { return append([]string(nil), r.autoGroups...) }
func (r *IdentityRequest) Authorize(group, physicalModel string) error {
	allowedGroup := group == r.tokenGroup
	if r.tokenGroup == "auto" {
		for _, g := range r.autoGroups {
			if g == group {
				allowedGroup = true
				break
			}
		}
	}
	if !allowedGroup {
		return fmt.Errorf("%w: token group restriction", identityservice.ErrDenied)
	}
	if !r.ServiceAllowed(group) {
		return fmt.Errorf("%w: service unavailable", identityservice.ErrDenied)
	}
	// Scope/product lookup is exact. The historical Key wildcard vocabulary is
	// retained, but never used to resolve or broaden the product scope.
	if r.whitelistEnabled && !r.whitelist[physicalModel] && !r.whitelist[ratio_setting.FormatMatchingModelName(physicalModel)] {
		return fmt.Errorf("%w: token model whitelist", identityservice.ErrDenied)
	}
	return r.snapshot.Authorize(r.identity, group, physicalModel)
}

func AuthorizeRequestModel(c *gin.Context, group, physicalModel string) error {
	if r := RequestIdentity(c); r != nil {
		return r.Authorize(group, physicalModel)
	}
	return nil
}

// AuthorizeRequestChannel adds the live candidate intersection, including pins.
func AuthorizeRequestChannel(c *gin.Context, group, physicalModel string, channelID int) error {
	if RequestIdentity(c) == nil {
		return nil
	}
	if err := AuthorizeRequestModel(c, group, physicalModel); err != nil {
		return err
	}
	if !model.IsChannelEnabledForGroupModel(group, physicalModel, channelID) {
		return fmt.Errorf("%w: channel not a candidate", identityservice.ErrDenied)
	}
	return nil
}

func RequestModelVisible(c *gin.Context, groups []string, physicalModel string) bool {
	for _, g := range groups {
		if AuthorizeRequestModel(c, g, physicalModel) != nil {
			continue
		}
		candidate, err := model.GetRandomSatisfiedChannel(g, physicalModel, 0, nil)
		if err == nil && candidate != nil {
			return true
		}
	}
	return false
}
