package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/chatgptauth"
	"github.com/OrlojHQ/meridian/pkg/client"
)

// connectChatGPT runs Sign in with ChatGPT (ADR 0038). The daemon holds the
// PKCE verifier and exchanges the code; this command only relays the
// browser's loopback redirect, so no token ever reaches this machine.
func connectChatGPT(ctx context.Context, config *cliConfig, api *client.Client, connectionID string) error {
	request := &client.ChatGPTSignInRequest{Listen: client.NewOptBool(false)}
	if connectionID != "" {
		request.ConnectionId = client.NewOptString(connectionID)
	}
	signIn, err := api.StartChatGPTSignIn(ctx, request)
	if err != nil {
		return apiCallError(err)
	}
	authorization, err := url.Parse(signIn.AuthorizationUrl)
	if err != nil {
		return errors.New("daemon returned an invalid authorization URL")
	}
	state := authorization.Query().Get("state")
	_, _ = fmt.Fprintf(config.stdout, "Open this page in a browser on this computer and approve Meridian:\n\n  %s\n\n", signIn.AuthorizationUrl)

	var callback string
	listener, listenErr := net.Listen("tcp", chatgptauth.CallbackAddress)
	if listenErr == nil {
		_, _ = fmt.Fprintln(config.stdout, "Waiting for ChatGPT to redirect back…")
		callback, err = awaitChatGPTCallback(ctx, listener, state, time.Until(signIn.ExpiresAt))
		if err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(config.stdout, "Port %s is busy, so this command cannot catch the redirect.\nAfter approving, copy the address of the page that fails to load and paste it here:\n", chatgptauth.CallbackAddress)
		line, readErr := bufio.NewReader(config.stdin).ReadString('\n')
		if readErr != nil && line == "" {
			return errors.New("no redirected address was entered")
		}
		callback = strings.TrimSpace(line)
	}
	done, err := api.CompleteChatGPTSignIn(ctx, &client.ChatGPTSignInCompletion{CallbackUrl: callback}, client.CompleteChatGPTSignInParams{SignInId: signIn.ID})
	if err != nil {
		return apiCallError(err)
	}
	return writeJSON(config.stdout, done)
}

// awaitChatGPTCallback serves the loopback redirect once and returns its
// full address. Requests for another attempt are ignored.
func awaitChatGPTCallback(ctx context.Context, listener net.Listener, state string, wait time.Duration) (string, error) {
	if wait <= 0 {
		wait = time.Minute
	}
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+chatgptauth.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Query().Get("state") != state {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, "<!doctype html><meta charset=utf-8><p>This is not the sign-in Meridian is waiting for.</p>")
			return
		}
		message := "Approved. Return to your terminal; Meridian is finishing the sign-in."
		if r.URL.Query().Get("error") != "" {
			message = "ChatGPT sign-in did not complete. Return to your terminal."
		}
		_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Meridian</title><p>%s</p>", html.EscapeString(message))
		select {
		case result <- chatgptauth.RedirectURI + "?" + r.URL.RawQuery:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case callback := <-result:
		return callback, nil
	case <-timer.C:
		return "", errors.New("ChatGPT sign-in timed out; run the command again")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
