package privatestorage

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateDescriptorPolicy(t *testing.T) {
	user, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	owner := "O:" + user.String()
	allow := "(A;;FA;;;" + user.String() + ")"
	for _, tc := range []struct {
		name, body       string
		directory, valid bool
	}{
		{"user", owner + "D:P" + allow, false, true},
		{"system", owner + "D:P" + allow + "(A;;FA;;;SY)", false, true},
		{"inheritable", owner + "D:P(A;OICI;FA;;;" + user.String() + ")", true, true},
		{"everyone", owner + "D:P" + allow + "(A;;FR;;;WD)", false, false},
		{"admins", owner + "D:P" + allow + "(A;;FA;;;BA)", false, false},
		{"unprotected", owner + "D:" + allow, false, false},
		{"empty", owner + "D:P", false, false},
		{"null", owner + "D:NO_ACCESS_CONTROL", false, false},
		{"foreignOwner", "O:S-1-5-21-1-2-3-4567D:P" + allow, false, false},
		{"deny", owner + "D:P(D;;FR;;;WD)" + allow, false, false},
		{"readOnly", owner + "D:P(A;;FR;;;" + user.String() + ")", false, false},
		{"noninheritableDir", owner + "D:P" + allow, true, false},
		{"inheritOnly", owner + "D:P(A;OICIIO;FA;;;" + user.String() + ")", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkDescriptor(sd, tc.directory); (err == nil) != tc.valid {
				t.Fatalf("valid=%v,error=%v", tc.valid, err)
			}
		})
	}
}

func TestWindowsRefusesPublicACLWithoutRepair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "token")
	f, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	user, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, dir} {
		if err := windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("public directory accepted")
	}
	if err := MkdirAll(dir); err == nil {
		t.Fatal("existing public directory repaired")
	}
	f, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := CheckFile(f); err == nil {
		t.Fatal("public credential accepted")
	}
	after, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if after.String() != before.String() {
		t.Fatal("existing ACL changed")
	}
}

func TestWindowsRejectsReparsePoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	if err := CheckDir(link); err == nil {
		t.Fatal("directory link accepted")
	}
}
