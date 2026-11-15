package updatecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
)

var migrationPattern = regexp.MustCompile(`^[0-9]{3}_[a-zA-Z0-9_]+\.sql$`)

// Unknown or computed declarations remain unknown; no source is executed.
func writerAPI(source string) int {
	file, err := parser.ParseFile(token.NewFileSet(), "info.go", source, 0)
	if err != nil {
		return 0
	}
	for _, decl := range file.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range v.Names {
				if name.Name == "WriterAPIVersion" && len(v.Values) > i {
					if n, ok := v.Values[i].(*ast.BasicLit); ok && n.Kind == token.INT {
						value, _ := strconv.Atoi(n.Value)
						return value
					}
				}
			}
		}
	}
	return 0
}
