package domain

import (
	"net/url"
	"strings"
)

const Redacted = "[REDACTED]"

// ContainsSecretMaterial is conservative by design. Domain records carry
// references and parameter names, never credentials or raw configuration.
func ContainsSecretMaterial(s string) bool {
	lower := strings.ToLower(s)
	markers := []string{
		"do_not_leak", "begin private key", "begin rsa private key",
		"authorization: bearer ", "password=", "passwd=", "secret=",
		"token=", "api_key=", "apikey=", "client_secret=",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if u, err := url.Parse(s); err == nil && u.IsAbs() && u.User != nil {
		if _, set := u.User.Password(); set || u.User.Username() != "" {
			return true
		}
	}
	return false
}

// RedactString converts suspect material to the only literal redaction marker
// accepted by helpers. Validation still rejects unredacted inputs.
func RedactString(s string) string {
	if ContainsSecretMaterial(s) {
		return Redacted
	}
	return s
}
