package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestMemberRebindKeepsCompletionTokenPrivate(t *testing.T) {
	for _, scenario := range []string{"success", "cancel", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			const secret = "completion-token-canary"
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "" {
					t.Error("missing installation bearer")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/users/usr_bootstrap_owner/identity-rebind":
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"signInUrl": "https://issuer.example/recover", "token": secret, "expiresAt": time.Now().Add(10 * time.Minute).UTC()})
				case "/users/usr_bootstrap_owner/identity-rebind/complete":
					calls++
					var body struct {
						Token string `json:"token"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token != secret {
						t.Errorf("completion payload invalid: %v", err)
					}
					switch scenario {
					case "cancel":
						w.WriteHeader(http.StatusAccepted)
						cancel()
					case "conflict":
						w.WriteHeader(http.StatusConflict)
						_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"identity already belongs to a member"}}`)
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			var output, progress bytes.Buffer
			config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), stdout: &output, json: true}
			root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
			root.SetErr(&progress)
			root.AddCommand(newMemberCommand(config))
			root.SetArgs([]string{"member", "rebind-identity", "usr_bootstrap_owner"})
			err := root.ExecuteContext(ctx)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("command error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("completion calls = %d", calls)
			}
			if strings.Contains(output.String()+progress.String(), secret) {
				t.Fatal("printed recovery token")
			}
			if !strings.Contains(progress.String(), "https://issuer.example/recover") {
				t.Fatal("no browser sign-in instructions")
			}
			if scenario == "success" && !strings.Contains(output.String(), `"rebound":true`) {
				t.Fatalf("result = %s", output.String())
			}
		})
	}
}
