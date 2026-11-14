// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import "testing"

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
	p, err := PolicyFrom(map[string]string{LabelVerifyIdentity: "^x$"}, Policy{})
	if err != nil || p.VerifyIdentity != "^x$" {
		t.Errorf("%+v %v", p, err)
	}
}
