package agentsandbox

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type portForwarder struct {
	config       *rest.Config
	setupTimeout time.Duration
}

func NewPortForwarder(config *rest.Config) Forwarder {
	if config == nil {
		return nil
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &portForwarder{config: rest.CopyConfig(config), setupTimeout: timeout}
}

func (f *portForwarder) Forward(
	ctx context.Context,
	namespace, pod string,
	remote uint16,
) (ForwardSession, error) {
	if namespace == "" || pod == "" || remote == 0 {
		return nil, fmt.Errorf("invalid port-forward target")
	}
	transport, upgrader, err := spdy.RoundTripperFor(f.config)
	if err != nil {
		return nil, fmt.Errorf("create port-forward transport: %w", err)
	}
	host, err := url.Parse(f.config.Host)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes API host: %w", err)
	}
	host.Path = "/api/v1/namespaces/" + url.PathEscape(namespace) +
		"/pods/" + url.PathEscape(pod) + "/portforward"
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, host)
	stop := make(chan struct{})
	ready := make(chan struct{})
	var errorOutput bytes.Buffer
	forward, err := portforward.NewOnAddresses(
		dialer, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(int(remote))},
		stop, ready, &bytes.Buffer{}, &errorOutput,
	)
	if err != nil {
		return nil, fmt.Errorf("configure port-forward: %w", err)
	}
	sessionContext, cancel := context.WithCancel(ctx)
	session := &forwardSession{
		stop: stop, done: make(chan struct{}), cancel: cancel,
	}
	go func() {
		defer close(session.done)
		session.err = forward.ForwardPorts()
	}()
	go func() {
		select {
		case <-sessionContext.Done():
			session.stopOnce.Do(func() { close(stop) })
		case <-session.done:
		}
	}()
	timer := time.NewTimer(f.setupTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	case <-timer.C:
		_ = session.Close()
		return nil, fmt.Errorf("port-forward readiness timed out after %s", f.setupTimeout)
	case <-session.done:
		if session.err == nil {
			session.err = fmt.Errorf("port-forward stopped before readiness")
		}
		if errorOutput.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", session.err, bounded(errorOutput.String(), 512))
		}
		return nil, session.err
	case <-ready:
	}
	ports, err := forward.GetPorts()
	if err != nil || len(ports) != 1 || ports[0].Local == 0 {
		_ = session.Close()
		return nil, fmt.Errorf("resolve forwarded port: %w", err)
	}
	session.url = "http://127.0.0.1:" + strconv.Itoa(int(ports[0].Local))
	return session, nil
}

type forwardSession struct {
	url       string
	stop      chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	stopOnce  sync.Once
	closeOnce sync.Once
	err       error
	closeErr  error
}

func (s *forwardSession) URL() string { return s.url }

func (s *forwardSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.stopOnce.Do(func() { close(s.stop) })
		<-s.done
		s.closeErr = s.err
	})
	return s.closeErr
}
