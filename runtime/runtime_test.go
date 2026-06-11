package runtime

import (
	"testing"
)

func TestTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on", "Y"} {
		if !truthy(v) {
			t.Errorf("truthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if truthy(v) {
			t.Errorf("truthy(%q) = true, want false", v)
		}
	}
}

func TestSetConfiguredBinaryTreatsDefaultAsUnset(t *testing.T) {
	t.Cleanup(func() { SetConfiguredBinary("") })

	SetConfiguredBinary("container")
	if got := configured(); got != "" {
		t.Errorf(`configured() after SetConfiguredBinary("container") = %q, want ""`, got)
	}

	SetConfiguredBinary("/opt/fork/bin/container")
	if got := configured(); got != "/opt/fork/bin/container" {
		t.Errorf("configured() = %q, want the explicit path", got)
	}
}

// TestResolvePrecedence exercises the uncached resolve() directly so each
// case starts clean (Resolve() caches via sync.Once for the real process).
func TestResolvePrecedence(t *testing.T) {
	t.Cleanup(func() { SetConfiguredBinary("") })

	t.Run("K3C_CONTAINER_BINARY wins", func(t *testing.T) {
		SetConfiguredBinary("/configured/container")
		t.Setenv("K3C_CONTAINER_BINARY", "/explicit/container")
		t.Setenv("K3C_CONTAINER_FROM_PATH", "1")
		r, err := resolve()
		if err != nil {
			t.Fatal(err)
		}
		if r.Binary != "/explicit/container" {
			t.Errorf("Binary = %q, want /explicit/container", r.Binary)
		}
		if len(r.Env) != 0 {
			t.Errorf("Env = %v, want none", r.Env)
		}
	})

	t.Run("FROM_PATH over configured", func(t *testing.T) {
		SetConfiguredBinary("/configured/container")
		t.Setenv("K3C_CONTAINER_BINARY", "")
		t.Setenv("K3C_CONTAINER_FROM_PATH", "true")
		r, err := resolve()
		if err != nil {
			t.Fatal(err)
		}
		if r.Binary != "container" {
			t.Errorf("Binary = %q, want bare container", r.Binary)
		}
	})

	t.Run("configured binary", func(t *testing.T) {
		SetConfiguredBinary("/configured/container")
		t.Setenv("K3C_CONTAINER_BINARY", "")
		t.Setenv("K3C_CONTAINER_FROM_PATH", "")
		r, err := resolve()
		if err != nil {
			t.Fatal(err)
		}
		if r.Binary != "/configured/container" {
			t.Errorf("Binary = %q, want /configured/container", r.Binary)
		}
	})

	t.Run("fallback to PATH without bundle", func(t *testing.T) {
		SetConfiguredBinary("")
		t.Setenv("K3C_CONTAINER_BINARY", "")
		t.Setenv("K3C_CONTAINER_FROM_PATH", "")
		r, err := resolve()
		if err != nil {
			t.Fatal(err)
		}
		// In ordinary (non-bundled) test builds HasBundle() is false, so we
		// fall back to PATH with no extra env.
		if HasBundle() {
			t.Skip("built with bundled payload; PATH fallback not exercised")
		}
		if r.Binary != "container" {
			t.Errorf("Binary = %q, want bare container", r.Binary)
		}
		if len(r.Env) != 0 {
			t.Errorf("Env = %v, want none for PATH fallback", r.Env)
		}
	})
}
