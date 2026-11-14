package apiauth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalTokenFilesRoundTrip(t *testing.T) {
	value := PersonalTokenPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if !ValidPersonalToken(value) {
		t.Fatal("well-formed personal token refused")
	}
	for _, bad := range []string{"", PersonalTokenPrefix, PersonalTokenPrefix + "short", "mrd_xyz_" + strings.Repeat("A", 43)} {
		if ValidPersonalToken(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
	path := DefaultMemberTokenPath(filepath.Join(t.TempDir(), "config"))
	if err := WritePersonalTokenFile(path, value); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, %v", info.Mode(), err)
	}
	read, err := ReadTokenFile(path)
	if err != nil || read != value {
		t.Fatalf("read = %q, %v", read, err)
	}
	// A second login replaces the token.
	next := PersonalTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if err := WritePersonalTokenFile(path, next); err != nil {
		t.Fatal(err)
	}
	if read, err := ReadTokenFile(path); err != nil || read != next {
		t.Fatalf("replaced token = %q, %v", read, err)
	}
	if err := WritePersonalTokenFile(path, "not-a-token"); err == nil {
		t.Fatal("wrote a malformed token")
	}
	// The installation token still reads as before.
	installation := DefaultTokenPath(t.TempDir())
	if _, err := OpenOrCreateToken(installation); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(installation); err != nil {
		t.Fatalf("installation token = %v", err)
	}
}
