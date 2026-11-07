package httpapi

import (
	"net/http"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

type boardItemJSON struct {
	Capsule        capsuleJSON          `json:"capsule"`
	Thread         *threadJSON          `json:"thread,omitempty"`
	CurrentRun     *runJSON             `json:"currentRun,omitempty"`
	Status         string               `json:"status"`
	Since          string               `json:"since"`
	NeedsYou       string               `json:"needsYou,omitempty"`
	LatestDelivery *deliverySummaryJSON `json:"latestDelivery,omitempty"`
	Link           *threadLinkJSON      `json:"link,omitempty"`
	ChildRollup    *childRollupJSON     `json:"childRollup,omitempty"`
	Surfaced       bool                 `json:"surfaced"`
}

type childRollupJSON struct {
	Total    int `json:"total"`
	Working  int `json:"working"`
	Idle     int `json:"idle"`
	Done     int `json:"done"`
	NeedsYou int `json:"needsYou"`
	Failed   int `json:"failed"`
}

type deliverySummaryJSON struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	Action            string `json:"action"`
	PullRequestNumber int64  `json:"pullRequestNumber,omitempty"`
	PullRequestURL    string `json:"pullRequestUrl,omitempty"`
	UpdatedAt         string `json:"updatedAt"`
}

type boardPageJSON struct {
	Items     []boardItemJSON `json:"items"`
	Truncated bool            `json:"truncated"`
}

func (s *Server) getBoard(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	board, err := s.service.Board(request.Context(), app.BoardQuery{
		ProjectID: domain.ProjectID(query.Get("projectId")),
		Expand:    app.BoardExpand(query.Get("expand")),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	response := boardPageJSON{Items: make([]boardItemJSON, 0, len(board.Items)), Truncated: board.Truncated}
	for _, item := range board.Items {
		row := boardItemJSON{
			Capsule: capsuleResponse(item.Capsule), Status: string(item.Status),
			Since: formatOptionalTime(item.Since), NeedsYou: string(item.NeedsYou),
			Surfaced: item.Surfaced,
		}
		if item.Thread != nil {
			thread := s.threadResponse(request.Context(), *item.Thread)
			row.Thread = &thread
		}
		if item.Run != nil {
			run := runResponse(*item.Run)
			row.CurrentRun = &run
		}
		if delivery := item.LatestDelivery; delivery != nil {
			row.LatestDelivery = &deliverySummaryJSON{
				ID: string(delivery.ID), State: string(delivery.State), Action: string(delivery.Action),
				PullRequestNumber: delivery.ResultPullRequestNumber,
				PullRequestURL:    delivery.ResultPullRequestURL,
				UpdatedAt:         formatOptionalTime(delivery.UpdatedAt),
			}
		}
		if item.Link != nil {
			link := s.linkResponse(request, *item.Link)
			row.Link = &link
		}
		if rollup := item.Rollup; rollup != nil {
			row.ChildRollup = &childRollupJSON{
				Total: rollup.Total, Working: rollup.Working, Idle: rollup.Idle,
				Done: rollup.Done, NeedsYou: rollup.NeedsYou, Failed: rollup.Failed,
			}
		}
		response.Items = append(response.Items, row)
	}
	writeJSON(writer, http.StatusOK, response)
}
