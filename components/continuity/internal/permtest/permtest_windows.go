package permtest

import "golang.org/x/sys/windows"

func currentUser() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func setDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// expose keeps the owner's full control and adds Everyone read access.
func expose(path string) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	return setDACL(path, "D:P(A;;FA;;;"+user+")(A;;FR;;;WD)")
}

// deny installs a protected empty DACL, which grants no access to anyone.
// The owner keeps the implicit right to read and rewrite the DACL, so the
// saved descriptor can be restored, re-enabling inheritance if it applied.
func deny(path string) (func() error, error) {
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	acl, _, err := before.DACL()
	if err != nil {
		return nil, err
	}
	control, _, err := before.Control()
	if err != nil {
		return nil, err
	}
	if err := setDACL(path, "D:P"); err != nil {
		return nil, err
	}
	return func() error {
		protection := windows.SECURITY_INFORMATION(windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
		if control&windows.SE_DACL_PROTECTED != 0 {
			protection = windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|protection, nil, nil, acl, nil)
	}, nil
}
