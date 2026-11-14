package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type auditRecordJSON struct {
	Sequence   int64  `json:"sequence"`
	RecordedAt string `json:"recordedAt"`
	Principal  string `json:"principal"`
	UserID     string `json:"userId,omitempty"`
	CapsuleID  string `json:"capsuleId,omitempty"`
	Action     string `json:"action"`
	ObjectType string `json:"objectType"`
	ObjectID   string `json:"objectId"`
	Result     string `json:"result"`
}

type auditRecordPageJSON struct {
	Items      []auditRecordJSON `json:"items"`
	NextCursor int64             `json:"nextCursor"`
	More       bool              `json:"more"`
}

func (s *Server) listAuditRecords(writer http.ResponseWriter, request *http.Request) {
	after, err := strconv.ParseInt(defaultQuery(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(writer, fmt.Errorf("%w: invalid audit cursor", domain.ErrInvalid))
		return
	}
	limit := 50
	if value := request.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			writeError(writer, fmt.Errorf("%w: audit limit must be between 1 and 500", domain.ErrInvalid))
			return
		}
	}
	page, err := s.service.ListAuditRecords(request.Context(), after, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]auditRecordJSON, len(page.Items))
	for index, record := range page.Items {
		items[index] = auditRecordJSON{
			Sequence: record.Sequence, RecordedAt: record.RecordedAt.UTC().Format(time.RFC3339Nano),
			Principal: record.PrincipalKind, UserID: string(record.UserID), CapsuleID: string(record.CapsuleID),
			Action: string(record.Action), ObjectType: record.ObjectType, ObjectID: record.ObjectID,
			Result: string(record.Result),
		}
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, auditRecordPageJSON{Items: items, NextCursor: page.NextCursor, More: page.More})
}
