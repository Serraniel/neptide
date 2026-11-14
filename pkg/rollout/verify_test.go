// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"regexp"
	"testing"
)

func TestVerifyLabels(t *testing.T) {
	for v, ok := range map[string]bool{
		`^https://github\.com/acme/app/\.github/workflows/release\.yml@refs/tags/v.*$`: true,
		`https://github\.com/acme/`: false, // unanchored
		`^(unclosed$`:               false,
	} {
		_, err := PolicyFrom(map[string]string{LabelVerifyIdentity: v}, Policy{})
		if (err == nil) != ok {
			t.Errorf("%s: %v", v, err)
		}
	}
	if _, err := PolicyFrom(map[string]string{LabelVerifyIssuer: "http://issuer"}, Policy{}); err == nil {
		t.Error("an http issuer")
	}
	p, err := PolicyFrom(map[string]string{LabelVerifyIdentity: "^https://a/x|evil$"}, Policy{})
	if err != nil || p.VerifyIdentity != "^(?:https://a/x|evil)$" {
		t.Fatalf("%+v %v", p, err)
	}
	re := regexp.MustCompile(p.VerifyIdentity)
	if re.MatchString("https://attacker/evil") || !re.MatchString("evil") {
		t.Error("the alternation isn't anchored as a whole")
	}
}
