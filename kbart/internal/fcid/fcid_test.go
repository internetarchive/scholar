package fcid

import "testing"

// Known ident/UUID pairs verified against the live fatcat v2 API and the ES
// fatcat_container index.
var pairs = []struct {
	ident string
	uuid  string
}{
	{"container_2ujzwjsay5aohfmwlpyiyhmb7a", "d5139b26-40c7-40e3-9596-5bf08c1d81f8"},
	{"iznnn644szdwva7khyxqzc73bi", "465ad6fb-9c96-476a-83ea-3e2f0c8bfb0a"},
}

func TestToUUID(t *testing.T) {
	for _, p := range pairs {
		got, err := ToUUID(p.ident)
		if err != nil {
			t.Errorf("ToUUID(%q) error: %v", p.ident, err)
			continue
		}
		if got != p.uuid {
			t.Errorf("ToUUID(%q) = %q, want %q", p.ident, got, p.uuid)
		}
	}
}

func TestFromUUID(t *testing.T) {
	for _, p := range pairs {
		// FromUUID returns the bare base32 tail, without any type prefix.
		want := p.ident
		if i := len(want) - 26; i > 0 {
			want = want[i:]
		}
		got, err := FromUUID(p.uuid)
		if err != nil {
			t.Errorf("FromUUID(%q) error: %v", p.uuid, err)
			continue
		}
		if got != want {
			t.Errorf("FromUUID(%q) = %q, want %q", p.uuid, got, want)
		}
	}
}

func TestToUUIDInvalidLength(t *testing.T) {
	if _, err := ToUUID("container_tooshort"); err != ErrInvalidLength {
		t.Errorf("expected ErrInvalidLength, got %v", err)
	}
}
