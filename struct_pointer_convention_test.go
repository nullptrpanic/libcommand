package libcommand

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProjectStructsCrossFunctionBoundariesByPointer(t *testing.T) {
	t.Helper()

	fileSet := token.NewFileSet()
	packages := make(map[string][]*ast.File)
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && (entry.Name() == ".git" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		key := filepath.Dir(path) + "\x00" + file.Name.Name
		packages[key] = append(packages[key], file)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}

	structNames := make(map[string]map[string]bool)
	allStructNames := make(map[string]bool)
	for key, files := range packages {
		structNames[key] = make(map[string]bool)
		for _, file := range files {
			for _, declaration := range file.Decls {
				general, ok := declaration.(*ast.GenDecl)
				if !ok || general.Tok != token.TYPE {
					continue
				}
				for _, specification := range general.Specs {
					typeSpec := specification.(*ast.TypeSpec)
					if _, ok := typeSpec.Type.(*ast.StructType); ok {
						structNames[key][typeSpec.Name.Name] = true
						allStructNames[typeSpec.Name.Name] = true
					}
				}
			}
		}
	}

	changed := true
	for changed {
		changed = false
		for key, files := range packages {
			for _, file := range files {
				for _, declaration := range file.Decls {
					general, ok := declaration.(*ast.GenDecl)
					if !ok || general.Tok != token.TYPE {
						continue
					}
					for _, specification := range general.Specs {
						typeSpec := specification.(*ast.TypeSpec)
						if structNames[key][typeSpec.Name.Name] {
							continue
						}
						underlyingStruct := false
						switch expression := typeSpec.Type.(type) {
						case *ast.Ident:
							underlyingStruct = structNames[key][expression.Name]
						case *ast.SelectorExpr:
							underlyingStruct = allStructNames[expression.Sel.Name]
						}
						if underlyingStruct {
							structNames[key][typeSpec.Name.Name] = true
							allStructNames[typeSpec.Name.Name] = true
							changed = true
						}
					}
				}
			}
		}
	}

	var violations []string
	for key, files := range packages {
		for _, file := range files {
			var valueStructName func(ast.Expr) string
			valueStructName = func(expression ast.Expr) string {
				switch expression := expression.(type) {
				case *ast.Ident:
					if structNames[key][expression.Name] {
						return expression.Name
					}
				case *ast.SelectorExpr:
					if allStructNames[expression.Sel.Name] {
						return expression.Sel.Name
					}
				case *ast.ParenExpr:
					return valueStructName(expression.X)
				case *ast.ArrayType:
					return valueStructName(expression.Elt)
				case *ast.Ellipsis:
					return valueStructName(expression.Elt)
				case *ast.MapType:
					if name := valueStructName(expression.Key); name != "" {
						return name
					}
					return valueStructName(expression.Value)
				case *ast.ChanType:
					return valueStructName(expression.Value)
				}
				return ""
			}
			checkFields := func(label string, fields *ast.FieldList) {
				if fields == nil {
					return
				}
				for _, field := range fields.List {
					name := valueStructName(field.Type)
					if name == "" {
						continue
					}
					position := fileSet.Position(field.Pos())
					violations = append(violations, position.String()+": value-struct "+label+" "+name)
				}
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if ok {
					checkFields("receiver", function.Recv)
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				function, ok := node.(*ast.FuncType)
				if ok {
					checkFields("parameter", function.Params)
					checkFields("result", function.Results)
				}
				return true
			})
		}
	}
	if len(violations) != 0 {
		slices.Sort(violations)
		t.Fatalf("project-defined structs must cross function boundaries by pointer:\n%s", strings.Join(violations, "\n"))
	}
}
