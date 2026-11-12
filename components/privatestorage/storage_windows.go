package privatestorage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fullControl = 0x1f01ff

func currentSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

// The protected DACL grants only this process's user and LocalSystem access.
// Directory ACEs inherit to SQLite journals and other new children.
func descriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	return windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;" + flags + ";FA;;;" + sid.String() + ")(A;" + flags + ";FA;;;SY)")
}

func MkdirAll(path string) error {
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return CheckDir(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return errors.New("cannot create private storage root")
	}
	// Existing ancestors need not be private. Only newly created directories
	// receive a DACL; never rewrite an existing project's permissions.
	if _, err := os.Lstat(parent); os.IsNotExist(err) {
		if err := MkdirAll(parent); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	sd, err := descriptor(true)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(p, &sa)
	runtime.KeepAlive(sd)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	return CheckDir(path)
}

func openHandle(path string, access, disposition, flags uint32, sd *windows.SECURITY_DESCRIPTOR) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var sa *windows.SecurityAttributes
	if sd != nil {
		sa = &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		sa, disposition, flags|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, &os.PathError{Op: "open private storage", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func CheckDir(path string) error {
	f, err := openHandle(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkHandle(f, true)
}

func Create(path string) (*os.File, error) {
	sd, err := descriptor(false)
	if err != nil {
		return nil, err
	}
	f, err := openHandle(path, windows.GENERIC_WRITE|windows.READ_CONTROL, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, sd)
	if err != nil {
		return nil, err
	}
	if err := checkFile(f); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return f, nil
}

func Open(path string) (*os.File, error) {
	return openHandle(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
}

func checkFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("private storage must be a regular file")
	}
	return checkHandle(f, false)
}

func checkHandle(f *os.File, directory bool) error {
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("private storage cannot be a reparse point or have the wrong object type")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return checkDescriptor(sd, directory)
}

func checkDescriptor(sd *windows.SECURITY_DESCRIPTOR, directory bool) error {
	user, err := currentSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user) {
		return errors.New("private storage must be owned by the current user")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private storage requires a protected Windows DACL")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount == 0 {
		return errors.New("private storage requires a nonempty Windows DACL")
	}
	userAccess := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		// Refuse unfamiliar/conditional/object ACEs rather than guessing their
		// effective permissions or relying on deny/allow ordering.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("private storage requires simple allow-only Windows ACEs")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || uintptr(ace.Header.AceSize) < unsafe.Offsetof(ace.SidStart)+uintptr(sid.Len()) {
			return errors.New("invalid Windows access control entry")
		}
		if !sid.Equals(user) && sid.String() != "S-1-5-18" {
			return errors.New("private storage grants access outside the current user and LocalSystem")
		}
		if sid.Equals(user) && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && uint32(ace.Mask)&fullControl == fullControl {
			if !directory || ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
				userAccess = true
			}
		}
	}
	if !userAccess {
		return errors.New("private storage requires current-user full control, inheritable for directories")
	}
	return nil
}
