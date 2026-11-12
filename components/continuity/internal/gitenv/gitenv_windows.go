package gitenv

// Continuity's managed clones nest 64-hex staging and retained-working names
// under the user's data directory, so object paths can exceed Windows MAX_PATH
// (260 characters) even when the clone root is short. core.longpaths lets Git
// for Windows use extended-length paths for these repositories without
// requiring a machine-wide setting.
var platformArgs = []string{"-c", "core.longpaths=true"}
