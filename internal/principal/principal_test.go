package principal

import (
	"context"
	"errors"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestZeroPrincipalIsNotAPrincipal(t *testing.T) {
	if (Principal{}).Valid() {
		t.Fatal("zero Principal is valid")
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("bare context carries a principal")
	}
	if _, ok := From(With(context.Background(), Principal{})); ok {
		t.Fatal("zero Principal attached as a principal")
	}
	if _, ok := From(nil); ok {
		t.Fatal("nil context carries a principal")
	}
	if _, err := Require(context.Background()); !errors.Is(err, ErrMissing) {
		t.Fatalf("Require on bare context = %v, want ErrMissing", err)
	}
}

func TestConstructors(t *testing.T) {
	if _, ok := User(""); ok {
		t.Fatal("User accepted an empty id")
	}
	if _, ok := Capsule("", "cap_1"); ok {
		t.Fatal("Capsule accepted an empty owner")
	}
	if _, ok := Capsule(BootstrapOwner, ""); ok {
		t.Fatal("Capsule accepted an empty Capsule id")
	}
	owner := Owner()
	if owner.Kind() != KindUser || owner.UserID() != BootstrapOwner || owner.CapsuleID() != "" {
		t.Fatalf("Owner() = %+v", owner)
	}
	capsule, ok := Capsule(BootstrapOwner, domain.CapsuleID("cap_1"))
	if !ok || capsule.Kind() != KindCapsule || capsule.UserID() != BootstrapOwner || capsule.CapsuleID() != "cap_1" {
		t.Fatalf("Capsule() = %+v, %v", capsule, ok)
	}
	if system := System(); system.Kind() != KindSystem || system.UserID() != "" {
		t.Fatalf("System() = %+v", system)
	}
	if admin := BreakGlass(); admin.Kind() != KindBreakGlass || admin.UserID() != "" {
		t.Fatalf("BreakGlass() = %+v", admin)
	}
}

func TestWithoutClearsAnInheritedPrincipal(t *testing.T) {
	ctx := WithSystem(context.Background())
	if p, ok := From(ctx); !ok || p.Kind() != KindSystem {
		t.Fatalf("From(WithSystem) = %+v, %v", p, ok)
	}
	if _, ok := From(Without(ctx)); ok {
		t.Fatal("Without kept the parent's principal")
	}
	if p, ok := From(With(Without(ctx), Owner())); !ok || p != Owner() {
		t.Fatalf("From(With(Owner)) = %+v, %v", p, ok)
	}
}

func TestKindString(t *testing.T) {
	for kind, want := range map[Kind]string{
		kindNone: "none", KindUser: "user", KindBreakGlass: "break_glass",
		KindSystem: "system", KindCapsule: "capsule",
	} {
		if got := kind.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}
