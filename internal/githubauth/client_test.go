package githubauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceFlowAndRepoList(t *testing.T) {
	var sawToken bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.RawQuery, "token=") || strings.Contains(string(body), "gho_secret") && r.URL.Path != "/login/oauth/access_token" {
			t.Errorf("token leaked into %s", r.URL)
		}
		if r.Header.Get("Authorization") == "Bearer gho_secretvalue" {
			sawToken = true
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			if !strings.Contains(string(body), "scope=repo+read%3Auser") && !strings.Contains(string(body), "scope=repo+read:user") {
				t.Fatalf("scope body = %s", body)
			}
			_, _ = io.WriteString(w, `{"device_code":"device-secret","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		case "/login/oauth/access_token":
			if strings.Contains(string(body), "device-secret") {
				_, _ = io.WriteString(w, `{"access_token":"gho_secretvalue","token_type":"bearer","scope":"repo read:user"}`)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "repo, read:user")
			_, _ = io.WriteString(w, `{"login":"octocat"}`)
		case "/user/repos":
			_, _ = io.WriteString(w, `[{"full_name":"octocat/hello","private":true,"description":"hi","default_branch":"main","html_url":"https://github.com/octocat/hello"}]`)
		case "/repos/octocat/private":
			_, _ = io.WriteString(w, `{"full_name":"octocat/private","private":true,"description":"secret repo","default_branch":"main","html_url":"https://github.com/octocat/private"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.Client())
	client.DeviceURL = server.URL + "/login/device/code"
	client.TokenURL = server.URL + "/login/oauth/access_token"
	client.APIBase = server.URL

	code, err := client.RequestDeviceCode(context.Background(), "Iv1.public")
	if err != nil || code.UserCode != "ABCD-EFGH" || code.DeviceCode != "device-secret" {
		t.Fatalf("device code = %+v, %v", code, err)
	}
	poll, err := client.PollDeviceToken(context.Background(), "Iv1.public", code.DeviceCode)
	if err != nil || poll.Token != "gho_secretvalue" || poll.Pending {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	account, err := client.CurrentUser(context.Background(), poll.Token)
	if err != nil || account.Login != "octocat" || !strings.Contains(account.Scopes, "repo") {
		t.Fatalf("account = %+v, %v", account, err)
	}
	repos, err := client.ListRepos(context.Background(), poll.Token, "octocat/private")
	if err != nil || len(repos) == 0 || repos[0].FullName != "octocat/private" || !repos[0].Private {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	if !sawToken {
		t.Fatal("user lookup did not send the bearer token")
	}
}

func TestRejectedLoginIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
	}))
	defer server.Close()
	client := New(server.Client())
	client.APIBase = server.URL
	_, err := client.CurrentUser(context.Background(), "gho_deadtoken")
	var status *StatusError
	if err == nil || !errorAs(err, &status) || status.Status != 401 || status.Reason != "invalid" {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "gho_deadtoken") {
		t.Fatal("token leaked into the error")
	}
}

func errorAs(err error, target **StatusError) bool {
	status, ok := err.(*StatusError)
	if ok {
		*target = status
	}
	return ok
}

func TestTokenShape(t *testing.T) {
	if ValidToken("") || ValidToken("short") || ValidToken("has space token") || ValidToken("line\nbreak") {
		t.Fatal("accepted a bad token")
	}
	if !ValidToken("gho_abcdefghijklmnopqrstuvwxyz") {
		t.Fatal("rejected a normal token")
	}
}
