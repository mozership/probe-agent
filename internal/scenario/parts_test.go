package scenario

import (
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	s := newSecrets(map[string]string{"PW": "abc", "LONG": "abc-and-more", "EMPTY": ""})

	if got := s.mask("pw abc, long abc-and-more"); got != "pw {secret:PW}, long {secret:LONG}" {
		t.Errorf("mask = %q", got)
	}
	if got := s.mask("nothing here"); got != "nothing here" {
		t.Errorf("an empty secret masked text: %q", got)
	}
	got, err := s.fill("{secret:PW}/{secret:LONG}/{secret:EMPTY}")
	if err != nil || got != "abc/abc-and-more/" {
		t.Errorf("fill = %q, %v", got, err)
	}
	if _, err := s.fill("{secret:NOPE}"); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("fill of an unknown secret = %v", err)
	}
	if got := s.maskAny(map[string]any{"a": []any{"abc", 1.0}}); got.(map[string]any)["a"].([]any)[0] != "{secret:PW}" {
		t.Errorf("maskAny = %v", got)
	}
}

func TestHostsAllow(t *testing.T) {
	h := hosts{"app.test:8080", "auth.test", "*.cdn.test"}
	for url, want := range map[string]bool{
		"http://app.test:8080/signup":  true,
		"http://app.test:9090/":        false,
		"https://auth.test/login":      true,
		"https://auth.test:8443/login": true,
		"https://img.cdn.test/a.png":   true,
		"https://evil.test/":           false,
		"https://cdn.test.evil.test/":  false,
		"about:blank":                  true,
		"javascript:alert(1)":          false,
		"file:///etc/passwd":           false,
	} {
		if got := h.allows(url); got != want {
			t.Errorf("allows(%q) = %v; want %v", url, got, want)
		}
	}
}

func TestFind(t *testing.T) {
	lines := []string{
		`heading "Create your account" level=1 [ref_1]`,
		`text "Already have one?"`,
		`textbox "Email" type=email [ref_2]`,
		`textbox "Password" type=password [ref_3]`,
		`button "Sign up" [ref_4]`,
		`link "Sign in" [ref_5]`,
	}
	got := find(lines, "email field")
	if len(got) == 0 || !strings.Contains(got[0], "Email") {
		t.Errorf("find(email field) = %q", got)
	}
	got = find(lines, "sign up button")
	if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), `button "Sign up"`) {
		t.Errorf("find(sign up button) = %q", got)
	}
	if got := find(lines, "already"); len(got) != 0 {
		t.Errorf("a text without a ref was found: %q", got)
	}
}

func TestNormalize(t *testing.T) {
	if normalize(" \"Welcome,\n  Alice\" ") != "welcome, alice" {
		t.Errorf("normalize = %q", normalize(" \"Welcome,\n  Alice\" "))
	}
}
