package release

import "testing"

func TestCompareAndNewer(t *testing.T) {
	cases := []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"v0.1.2", "v0.1.1", 1, true},
		{"v0.1.1", "v0.1.2", -1, true},
		{"0.1.1", "v0.1.1", 0, true},
		{"v0.1.10", "v0.1.9", 1, true},
		{"v1.0.0", "v0.99.99", 1, true},
		{"v0.2.0-rc1", "v0.2.0", 0, true},
		{"dev", "v0.1.1", 0, false},
		{"v0.1", "v0.1.1", 0, false},
		{"", "v0.1.1", 0, false},
	}
	for _, c := range cases {
		cmp, ok := Compare(c.a, c.b)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, cmp, ok, c.cmp, c.ok)
		}
	}
	if !Newer("v0.1.2", "v0.1.1") || Newer("v0.1.1", "v0.1.1") || Newer("v0.1.0", "v0.1.1") {
		t.Error("Newer ordered versions wrongly")
	}
	if Newer("v9.9.9", "dev") || Newer("dev", "v0.1.1") {
		t.Error("a build that is not a version must never be offered an update")
	}
}
