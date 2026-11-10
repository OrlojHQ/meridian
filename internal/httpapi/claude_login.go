package httpapi

import (
	"net/http"
)

func (s *Server) beginClaudeLogin(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ConnectionID string `json:"connectionId"`
	}
	if !decode(writer, request, &body) {
		return
	}
	started, err := s.service.BeginClaudeLogin(request.Context(), request.Host, body.ConnectionID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, started)
}

func (s *Server) getClaudeLogin(writer http.ResponseWriter, request *http.Request) {
	status, err := s.service.ClaudeLoginStatus(request.Context(), request.PathValue("loginId"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) completeClaudeLogin(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decode(writer, request, &body) {
		return
	}
	status, err := s.service.CompleteClaudeLogin(request.Context(), request.PathValue("loginId"), body.Code)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, status)
}

// claudeLoginCallback is the loopback redirect Claude sends the browser to.
// It is unauthenticated. The single-use state and the in-memory PKCE verifier
// authorize the exchange. The response never includes the code or the tokens.
func (s *Server) claudeLoginCallback(writer http.ResponseWriter, request *http.Request) {
	ok := false
	if len(request.URL.RawQuery) <= 4096 {
		query := request.URL.Query()
		if query.Get("error") == "" && query.Get("code") != "" && query.Get("state") != "" {
			ok = s.service.CompleteClaudeCallback(request.Context(), query.Get("state"), query.Get("code")) == nil
		}
	}
	writeClaudeHandoff(writer, ok)
}

func (s *Server) claudeLoginFinished(writer http.ResponseWriter, request *http.Request) {
	writeClaudeFinished(writer, request.URL.Query().Get("result") == "ok")
}

func writeClaudeHandoff(writer http.ResponseWriter, ok bool) {
	result := "failed"
	if ok {
		result = "ok"
	}
	writeClaudePage(writer, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Claude sign-in</title></head>
<body><p>Returning to Meridian.</p><script>location.replace("/claude-login/finished?result=`+result+`");</script></body></html>`)
}

func writeClaudeFinished(writer http.ResponseWriter, ok bool) {
	message := "Claude sign-in did not finish. Close this window and try again from Meridian."
	if ok {
		message = "Signed in. You can close this window and return to Meridian."
	}
	writeClaudePage(writer, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Claude sign-in</title></head>
<body><p>`+message+`</p><script>if(window.opener){window.close()}else{location.replace("/ui/settings/harnesses")}</script></body></html>`)
}

func writeClaudePage(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(body))
}
