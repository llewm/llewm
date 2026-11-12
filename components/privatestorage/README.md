# Private storage

`github.com/llewm/llewm/components/privatestorage` is a small Go library shared by
Continuity and orchestration. It creates and verifies files and directories that
only the current user may access, and serializes cooperating writers with OS
file locks.

- Unix: directories are created `0700` and files `0600`; checks reject group or
  other permission bits.
- Windows: new objects get a protected DACL granting full control only to the
  current user (inheritable for directories) and LocalSystem. Checks reject a
  foreign owner, an unprotected or empty DACL, deny or non-simple ACEs, and any
  grant to another principal.
- Reparse points (symbolic links, junctions) and the wrong object type are
  rejected by inspecting the opened handle, not a second path lookup.
- Existing objects are never repaired: a non-private object is an error.
- `Lock` holds an exclusive OS lock on a private lock file; process exit releases it.

The module depends only on `golang.org/x/sys`. Consumers reference it with a
`replace` to `../privatestorage`; see [source publication](../../docs/source-publication.md).

From this directory:

```sh
go fmt ./...
go test ./...
go vet ./...
```

Windows symbolic-link coverage skips only when the user lacks the symlink
privilege; junction coverage runs for ordinary users.
