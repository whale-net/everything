package mcpauth

import "testing"

// TestVerifyCodeChallenge_RFC7636AppendixBVector pins verifyCodeChallenge
// against the known-answer test vector from RFC 7636 Appendix B: a fixed
// code_verifier and its S256 code_challenge.
func TestVerifyCodeChallenge_RFC7636AppendixBVector(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)

	if !verifyCodeChallenge(verifier, challenge) {
		t.Fatalf("verifyCodeChallenge(%q, %q) = false, want true (RFC 7636 Appendix B)", verifier, challenge)
	}
}

func TestVerifyCodeChallenge_WrongVerifier_Fails(t *testing.T) {
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	if verifyCodeChallenge("not-the-right-verifier", challenge) {
		t.Fatal("verifyCodeChallenge must reject a verifier that does not hash to challenge")
	}
}

func TestVerifyCodeChallenge_EmptyInputs_Fails(t *testing.T) {
	if verifyCodeChallenge("", "") {
		t.Fatal("verifyCodeChallenge(\"\", \"\") must not be true — an empty verifier's hash is never an empty challenge")
	}
}

func TestVerifyCodeChallenge_DifferentLengthChallenge_Fails(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

	if verifyCodeChallenge(verifier, "short") {
		t.Fatal("verifyCodeChallenge must reject a challenge of the wrong length rather than panicking or comparing garbage")
	}
}
