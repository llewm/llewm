package privatestorage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// makeJunction turns the existing empty directory link into a mount-point
// reparse point (an NTFS junction) targeting the absolute directory target.
// Unlike symbolic links, junctions need no SeCreateSymbolicLinkPrivilege or
// developer mode, so ordinary users exercise real reparse rejection. Both paths
// must be inside test-owned temporary directories; the link is removed on
// cleanup without following it.
func makeJunction(t *testing.T, target, link string) {
	t.Helper()
	target, err := filepath.Abs(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove junction fixture: %v", err)
		}
	})
	substitute := utf16.Encode([]rune(`\??\` + target))
	printName := utf16.Encode([]rune(target))
	// REPARSE_DATA_BUFFER for IO_REPARSE_TAG_MOUNT_POINT: an 8-byte header,
	// four uint16 name offsets/lengths, then NUL-terminated UTF-16 names.
	pathBytes := (len(substitute) + 1 + len(printName) + 1) * 2
	buffer := make([]byte, 8+8+pathBytes)
	binary.LittleEndian.PutUint32(buffer[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(8+pathBytes))
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16(len(substitute)*2))
	binary.LittleEndian.PutUint16(buffer[12:], uint16((len(substitute)+1)*2))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(len(printName)*2))
	offset := 16
	for _, unit := range substitute {
		binary.LittleEndian.PutUint16(buffer[offset:], unit)
		offset += 2
	}
	offset += 2
	for _, unit := range printName {
		binary.LittleEndian.PutUint16(buffer[offset:], unit)
		offset += 2
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("open junction fixture: %v", err)
	}
	defer windows.CloseHandle(handle)
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, (*byte)(unsafe.Pointer(&buffer[0])),
		uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Fatalf("create junction fixture without symlink privilege: %v", err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		t.Fatal(err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatal("junction fixture is not a reparse point")
	}
}

func TestWindowsRejectsJunctionWithoutSymlinkPrivilege(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "private")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err != nil {
		t.Fatalf("real private directory rejected: %v", err)
	}
	// Give the link the same private DACL and object type as a valid private
	// directory, so only the reparse-point guard can reject the junction.
	link := filepath.Join(root, "junction")
	if err := MkdirAll(link); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(link); err != nil {
		t.Fatalf("private link directory rejected before conversion: %v", err)
	}
	makeJunction(t, dir, link)

	if err := CheckDir(link); err == nil {
		t.Fatal("directory junction accepted by CheckDir")
	}
	if err := MkdirAll(link); err == nil {
		t.Fatal("existing directory junction accepted by MkdirAll")
	}
	f, err := Open(link)
	if err != nil {
		t.Fatalf("open junction without following: %v", err)
	}
	defer f.Close()
	if err := CheckFile(f); err == nil {
		t.Fatal("directory junction accepted by CheckFile")
	}
	if err := CheckDir(dir); err != nil {
		t.Fatalf("junction target changed: %v", err)
	}
}
