package instance_credential

import "testing"

func TestDigesterIsDeterministicAndKeyed(t *testing.T) {
	first, err := NewDigester([]byte("0123456789abcdef0123456789abcdef"), 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDigester([]byte("abcdef0123456789abcdef0123456789"), 3)
	if err != nil {
		t.Fatal(err)
	}
	one, version, err := first.Digest("secret-token")
	if err != nil || version != 3 || len(one) != 64 {
		t.Fatalf("Digest() = %q, %d, %v", one, version, err)
	}
	again, _, _ := first.Digest("secret-token")
	other, _, _ := second.Digest("secret-token")
	if one != again || one == other || one == "secret-token" {
		t.Fatal("digest is not stable, keyed, and one-way")
	}
}

func TestDigesterRejectsUnsafeConfigurationAndEmptyTokens(t *testing.T) {
	if _, err := NewDigester([]byte("short"), 1); err == nil {
		t.Fatal("short key was accepted")
	}
	if _, err := NewDigester(make([]byte, 32), 0); err == nil {
		t.Fatal("invalid key version was accepted")
	}
	digester, _ := NewDigester(make([]byte, 32), 1)
	if _, _, err := digester.Digest(""); err == nil {
		t.Fatal("empty token was accepted")
	}
}
