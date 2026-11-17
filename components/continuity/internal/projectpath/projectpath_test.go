package projectpath

import "testing"

// The legacy value a Windows writer stored for a URL entered as a roadmap
// source path; Linux filepath rules rejected it, Windows rules accepted it.
const windowsGuardedURL = "./https:/github.com/llewm/llewm/issues"

func TestValidAndAdmittedAreHostIndependent(t *testing.T) {
	for _, test := range []struct {
		value           string
		valid, admitted bool
	}{
		{"docs/roadmap.md", true, true},
		{"a", true, true},
		{"docs/a:b.md", true, true},
		{`docs\x.md`, true, true},
		{windowsGuardedURL, false, true},
		{"./C:/x", false, true},
		{"https:/github.com/x", false, true},
		{"C:/x", false, true},
		{"C:x", false, true},
		{".", false, true},
		{"", false, false},
		{"..", false, false},
		{"../x", false, false},
		{"/x", false, false},
		{"//server/share", false, false},
		{"./docs/x.md", false, false},
		{"./x", false, false},
		{"./../x:y", false, false},
		{"./.", false, false},
		{"./x:y/../z", false, false},
		{"docs/../x", false, false},
		{"docs//x", false, false},
		{"docs/", false, false},
		{"a\x00b", false, false},
	} {
		if got := Valid(test.value); got != test.valid {
			t.Errorf("Valid(%q) = %v, want %v", test.value, got, test.valid)
		}
		if got := Admitted(test.value); got != test.admitted {
			t.Errorf("Admitted(%q) = %v, want %v", test.value, got, test.admitted)
		}
		if test.valid && !test.admitted {
			t.Errorf("%q is writable but not admissible", test.value)
		}
	}
}

func TestNormalizeProducesSlashCleanedPaths(t *testing.T) {
	for _, test := range []struct {
		value, want string
		ok          bool
	}{
		{"docs/./roadmap.md", "docs/roadmap.md", true},
		{"docs/x/../roadmap.md", "docs/roadmap.md", true},
		{"./docs/roadmap.md", "docs/roadmap.md", true},
		{"https://github.com/llewm/llewm/issues", "https:/github.com/llewm/llewm/issues", false},
		{"/etc/passwd", "/etc/passwd", false},
		{"../x", "../x", false},
		{"", ".", false},
	} {
		got, ok := Normalize(test.value)
		if got != test.want || ok != test.ok {
			t.Errorf("Normalize(%q) = %q, %v; want %q, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}
