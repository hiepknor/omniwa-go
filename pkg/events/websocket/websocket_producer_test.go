package websocket_producer

import "testing"

func TestTokenFromProtocolHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"valid with space", "apikey, secret123", "secret123"},
		{"valid without space", "apikey,secret123", "secret123"},
		{"token surrounded by spaces is trimmed", "apikey,  secret123 ", "secret123"},
		{"extra protocols after token are ignored", "apikey, secret123, foo", "secret123"},
		{"empty header", "", ""},
		{"only the scheme, no token", "apikey", ""},
		{"scheme present but token empty", "apikey, ", ""},
		{"wrong scheme", "bearer, secret123", ""},
		{"scheme not first", "foo, apikey, secret123", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TokenFromProtocolHeader(c.header); got != c.want {
				t.Errorf("TokenFromProtocolHeader(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}
