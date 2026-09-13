package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const identityServiceBodyLimit = 2 << 20

type identityServiceRequest struct {
	ExpectedRevision *uint64
	Config           identityservice.Config
	Migration        *identityservice.MigrationInput
}

// Read the small envelope explicitly: duplicate or case-aliased fields cannot
// turn a rejected first config/revision into an accepted last one.
func decodeIdentityServiceRequest(c *gin.Context) (identityServiceRequest, error) {
	var out identityServiceRequest
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, identityServiceBodyLimit))
	t, err := d.Token()
	if err != nil {
		return out, err
	}
	if t != json.Delim('{') {
		return out, errors.New("request must be an object")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return out, err
		}
		key, ok := token.(string)
		if !ok {
			return out, errors.New("invalid request field")
		}
		if seen[key] {
			return out, errors.New("duplicate request field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return out, err
		}
		switch key {
		case "expected_revision":
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return out, errors.New("expected_revision cannot be null")
			}
			var revision uint64
			if err = json.Unmarshal(raw, &revision); err != nil {
				return out, err
			}
			out.ExpectedRevision = &revision
		case "config":
			out.Config, err = identityservice.Decode(raw)
			if err != nil {
				return out, err
			}
		case "migration_source":
			migration, err := identityservice.DecodeMigration(raw)
			if err != nil {
				return out, err
			}
			out.Migration = &migration
		default:
			return out, errors.New("unknown request field: " + key)
		}
	}
	if _, err = d.Token(); err != nil {
		return out, err
	}
	if _, err = d.Token(); err != io.EOF {
		return out, errors.New("trailing JSON")
	}
	if !seen["config"] {
		return out, errors.New("config is required")
	}
	return out, nil
}
func identityServiceError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, model.ErrIdentityServiceConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, identityservice.ErrActivationPending) {
		status = http.StatusUnprocessableEntity
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error(), "activation_ready": false})
}
func GetIdentityServiceSetting(c *gin.Context) {
	if err := model.IdentityServiceSettings.Refresh(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "identity/service configuration unavailable", "activation_ready": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": model.IdentityServiceSettings.Snapshot().Config(), "activation_ready": false})
}
func PutIdentityServiceSetting(c *gin.Context) {
	req, err := decodeIdentityServiceRequest(c)
	if err != nil {
		identityServiceError(c, err)
		return
	}
	if req.ExpectedRevision == nil {
		identityServiceError(c, errors.New("expected_revision is required"))
		return
	}
	var sources []identityservice.MigrationInput
	if req.Migration != nil {
		sources = append(sources, *req.Migration)
	}
	snap, err := model.IdentityServiceSettings.Save(req.Config, *req.ExpectedRevision, sources...)
	if err != nil {
		identityServiceError(c, err)
		return
	}
	recordManageAudit(c, "identity_service.update", map[string]interface{}{"revision": snap.Config().Revision, "mode": "legacy"})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": snap.Config(), "activation_ready": false})
}
func ValidateIdentityServiceSetting(c *gin.Context) {
	req, err := decodeIdentityServiceRequest(c)
	if err != nil {
		identityServiceError(c, err)
		return
	}
	var report *identityservice.MigrationReport
	if req.Migration != nil {
		r := identityservice.Migrate(*req.Migration)
		report = &r
	}
	if err = model.ValidateIdentityServiceDraft(req.Config, req.Migration); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, identityservice.ErrActivationPending) {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error(), "migration_report": report, "activation_blockers": service.IdentityActivationBlockers(req.Config), "activation_ready": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "config_valid": true, "migration_report": report, "activation_blockers": service.IdentityActivationBlockers(req.Config), "activation_ready": false, "pending_verification": []string{"production authorization matrix and all-component rounding/quota migration equivalence", "production backup/restore and rollback compatibility", "in-flight legacy tasks, old-instance writes and runtime validation", "explicit production and activation authorization"}})
}
