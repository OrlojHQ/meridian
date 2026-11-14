package app

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// operatorContext is a request from the single-user mode operator, the
// bootstrap owner. Tests that drive the Service directly use it the way
// internal/httpapi attaches it to an authenticated request.
func operatorContext() context.Context {
	return principal.With(context.Background(), principal.Owner())
}

// principalExempt lists the exported Service methods that do not require a
// principal, and why. Every other exported method takes a context first and
// fails closed without a principal. Adding a method here needs the same
// justification ADR 0046 requires: it reads or changes no object.
var principalExempt = map[string]string{
	// Daemon wiring, called once at startup before the API listens.
	"ConfigureChatGPT":              "startup wiring",
	"ConfigureCoordination":         "startup wiring",
	"ConfigureLocalProviderGateway": "startup wiring",
	"ConfigureProviderGateway":      "startup wiring",
	"ConfigureSecrets":              "startup wiring",
	"ConfigureObserver":             "startup wiring",
	"ConfigurePreview":              "startup wiring",
	"ConfigureBrowse":               "startup wiring",
	"ConfigureDelivery":             "startup wiring",
	"ConfigureThreads":              "startup wiring",
	"ConfigureHarnessProfiles":      "startup wiring",
	"ConfigureHarnessPacks":         "startup wiring",
	"ConfigureTemporal":             "startup wiring",
	"SetClaudeTokenURL":             "startup wiring",
	// Installation capability flags; they name no object.
	"ChatGPTAvailable":                 "capability flag",
	"CoordinationSupported":            "capability flag",
	"ProviderGatewayEnabled":           "capability flag",
	"ProviderGatewayOnAPIListener":     "capability flag",
	"ProviderGatewayAllowsPrivateHTTP": "capability flag",
	// The gateway handler authenticates each Capsule lease itself, and the
	// request then acts as that Capsule.
	"ProviderGateway": "lease-authenticated handler",
	// Daemon shutdown.
	"CloseChatGPT":         "shutdown",
	"CloseProviderGateway": "shutdown",
	// The unauthenticated loopback callback. Its single-use state is what
	// authorizes it, and it acts as the principal that began the sign-in.
	"CompleteClaudeCallback": "state-authorized; acts as the sign-in's starter",
}

var contextType = reflect.TypeFor[context.Context]()

// tripwire ports panic on any call: a method that reached one before
// checking its principal fails the test.
type tripStore struct{}

func (tripStore) View(context.Context, func(ports.Reader) error) error {
	panic("store read before principal check")
}

func (tripStore) Transact(context.Context, func(ports.Transaction) error) error {
	panic("store write before principal check")
}

func (tripStore) Close() error { panic("store closed before principal check") }

func tripwireService(t *testing.T) *Service {
	t.Helper()
	s := NewService(tripStore{}, struct{ ports.Clock }{}, struct{ ports.IDSource }{},
		struct{ ports.ReconcileQueue }{}, struct{ ports.CapsuleRuntime }{})
	s.ConfigureObserver(struct{ ports.Observer }{}, "tripwire")
	s.ConfigurePreview(struct{ ports.PreviewRuntime }{})
	s.ConfigureBrowse(struct{ ports.WorkspaceBrowser }{})
	s.ConfigureDelivery(struct{ ports.DeliveryRuntime }{}, struct{ GitHubClient }{})
	s.ConfigureThreads(struct{ ports.StructuredRuntime }{}, struct{ transcriptKey }{})
	s.ConfigureTemporal(struct{ ports.WorkspaceSnapshotter }{}, struct{ ports.ArtifactStore }{})
	s.ConfigureCoordination(struct{ ports.CoordinationRuntime }{}, 1)
	s.ConfigureChatGPT(t.TempDir(), nil)
	t.Cleanup(s.CloseChatGPT)
	return s
}

// TestEveryServiceMethodRejectsAMissingPrincipal enumerates the exported
// Service methods by reflection, so a new method is covered without editing
// a list. Each is called with no principal and zero arguments. It must not
// touch a port, and when it returns an error that error is ErrMissing.
func TestEveryServiceMethodRejectsAMissingPrincipal(t *testing.T) {
	serviceType := reflect.TypeFor[*Service]()
	for name := range principalExempt {
		if _, ok := serviceType.MethodByName(name); !ok {
			t.Errorf("principalExempt names %s, which is not a Service method", name)
		}
	}
	checked := 0
	for i := range serviceType.NumMethod() {
		method := serviceType.Method(i)
		if _, exempt := principalExempt[method.Name]; exempt {
			continue
		}
		methodType := method.Type
		if methodType.NumIn() < 2 || methodType.In(1) != contextType {
			t.Errorf("%s takes no context.Context first, so it cannot read a principal; "+
				"take one or justify an entry in principalExempt", method.Name)
			continue
		}
		checked++
		t.Run(method.Name, func(t *testing.T) {
			s := tripwireService(t)
			for _, ctx := range []context.Context{
				context.Background(),
				principal.Without(principal.WithSystem(context.Background())),
			} {
				results, panicked := callWithoutPrincipal(s, method, ctx)
				if panicked != nil {
					t.Fatalf("%s ran before checking its principal: %v", method.Name, panicked)
				}
				assertRejected(t, method, results)
			}
		})
	}
	if checked < 100 {
		t.Fatalf("checked %d Service methods; reflection missed most of them", checked)
	}
}

func callWithoutPrincipal(s *Service, method reflect.Method, ctx context.Context) (results []reflect.Value, panicked any) {
	defer func() { panicked = recover() }()
	methodType := method.Type
	args := []reflect.Value{reflect.ValueOf(s), reflect.ValueOf(&ctx).Elem()}
	for i := 2; i < methodType.NumIn(); i++ {
		args = append(args, reflect.Zero(methodType.In(i)))
	}
	if methodType.IsVariadic() {
		return method.Func.CallSlice(args), nil
	}
	return method.Func.Call(args), nil
}

func assertRejected(t *testing.T, method reflect.Method, results []reflect.Value) {
	t.Helper()
	errorType := reflect.TypeFor[error]()
	for i, result := range results {
		last := i == len(results)-1
		if last && result.Type() == errorType {
			err, _ := result.Interface().(error)
			if !errors.Is(err, principal.ErrMissing) {
				t.Fatalf("%s without a principal returned %v, want ErrMissing", method.Name, err)
			}
			if err.Error() != principal.ErrMissing.Error() {
				t.Fatalf("%s rejection is not content-free: %q", method.Name, err)
			}
			continue
		}
		if !result.IsZero() {
			t.Fatalf("%s without a principal returned non-zero result %d: %v", method.Name, i, result)
		}
	}
}

// TestServiceMethodsCheckThePrincipalFirst is the source-level half of the
// check above. A method that returns no error, or whose zero arguments
// happen to return early, could skip its guard and still pass the runtime
// test; this requires the guard to be the first statement.
func TestServiceMethodsCheckThePrincipalFirst(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var missing []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !isServiceReceiver(fn) {
				continue
			}
			if _, exempt := principalExempt[fn.Name.Name]; exempt {
				continue
			}
			if !startsWithPrincipalCheck(fn) {
				missing = append(missing, fmt.Sprintf("%s (%s)", fn.Name.Name, fset.Position(fn.Pos())))
			}
		}
	}
	sort.Strings(missing)
	for _, item := range missing {
		t.Errorf("%s does not check its principal first", item)
	}
}

func isServiceReceiver(fn *ast.FuncDecl) bool {
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Service"
}

// startsWithPrincipalCheck accepts `if err := requirePrincipal(ctx); ...`,
// `if requirePrincipal(ctx) != nil`, and `p, err := principal.Require(ctx)`.
func startsWithPrincipalCheck(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	found := false
	ast.Inspect(fn.Body.List[0], func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}
		switch callee := call.Fun.(type) {
		case *ast.Ident:
			found = found || callee.Name == "requirePrincipal"
		case *ast.SelectorExpr:
			pkg, ok := callee.X.(*ast.Ident)
			found = found || (ok && pkg.Name == "principal" && callee.Sel.Name == "Require")
		}
		return !found
	})
	return found
}
