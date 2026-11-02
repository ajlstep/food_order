package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
)

// codeAlphabet has 32 characters: letters and digits, without 0, O, 1, I,
// which look alike when read from a screen. 32 divides 256, so a random byte
// maps to a character without favouring any of them.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// CodeLength is the number of characters in a code.
const CodeLength = 4

// randomCode makes a new code with the system's secure random generator.
func randomCode() (string, error) {
	var raw [CodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random code: %w", err)
	}
	out := make([]byte, CodeLength)
	for i, b := range raw {
		out[i] = codeAlphabet[int(b)&(len(codeAlphabet)-1)]
	}
	return string(out), nil
}

// normalizeCode accepts what a person types: any case, with spaces or dashes.
// ok is false if the result cannot be a code.
func normalizeCode(s string) (code string, ok bool) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(s)))
	if len(s) != CodeLength {
		return "", false
	}
	for _, r := range s {
		if !strings.ContainsRune(codeAlphabet, r) {
			return "", false
		}
	}
	return s, true
}

// hashCode is what we store instead of the code. With only a million possible
// codes, a plain hash could be reversed in a second; the secret makes that
// impossible without it. The account and the email are in the hash so that a
// code is good only for the one it was made for.
func hashCode(secret []byte, userID, email, code string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(userID + "\x00" + email + "\x00" + code))
	return hex.EncodeToString(mac.Sum(nil))
}

// sameHash compares in constant time.
func sameHash(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// normalizeEmail lower-cases the address and checks that it is only an address.
func normalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s { // "Ana <ana@x.com>" is not just an address
		return "", false
	}
	_, domain, _ := strings.Cut(s, "@")
	if !strings.Contains(domain, ".") || strings.HasSuffix(domain, ".") || strings.ContainsAny(s, " ,;<>()") {
		return "", false
	}
	return s, true
}
