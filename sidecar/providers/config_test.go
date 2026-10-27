package providers

import "testing"

func TestSelectionsAndLegacyDefaults(t *testing.T) {
	c, err := Normalize("", "", "", false)
	if err != nil || c.Backends != "herdr" {
		t.Fatal(c, err)
	}
	c, err = Normalize("herdr", "", "default,work", false)
	if err != nil || c.HerdrPath != "herdr" || c.HerdrSessions != "default,work" {
		t.Fatal(c, err)
	}
	for _, v := range []struct {
		b, p, s string
		lab     bool
	}{
		{"unknown", "", "", false}, {"xirp,xirp", "", "", false}, {"xirp", "herdr", "", false},
		{"herdr", "./herdr", "", false}, {"herdr", "herdr", "../work", false}, {"herdr", "herdr", "work,work", false},
		{"herdr", "", "", true}, {"xirp,herdr", "", "", true},
	} {
		if _, err := Normalize(v.b, v.p, v.s, v.lab); err == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
}
