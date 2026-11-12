// Command symbols generates the read-only source symbol index used by
// PROJECT_MAP.md. It uses only the Go parser and never executes project code.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type symbol struct {
	name, file, pkg, responsibility string
}

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "", "output file (default stdout)")
	flag.Parse()
	if err := run(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "symbols:", err)
		os.Exit(1)
	}
}

func run(root, output string) error {
	var symbols []symbol
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, err := parseFile(root, path)
			if err != nil {
				return err
			}
			symbols = append(symbols, found...)
			return nil
		})
		if err != nil {
			return err
		}
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].file != symbols[j].file {
			return symbols[i].file < symbols[j].file
		}
		return symbols[i].name < symbols[j].name
	})

	var dst io.Writer = os.Stdout
	var file *os.File
	if output != "" {
		var err error
		file, err = os.Create(output)
		if err != nil {
			return err
		}
		dst = file
	}
	if err := write(dst, symbols); err != nil {
		if file != nil {
			_ = file.Close()
		}
		return err
	}
	if file != nil {
		return file.Close()
	}
	return nil
}

func parseFile(root, path string) ([]symbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	rel := filepath.ToSlash(mustRel(root, path))
	pkg := "repairflow/" + filepath.ToSlash(filepath.Dir(rel))
	var out []symbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil {
				name = receiverName(d.Recv) + "." + name
			}
			out = append(out, symbol{name: name, file: rel, pkg: pkg, responsibility: summary(d.Doc.Text())})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				var names []string
				var doc string
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names, doc = []string{s.Name.Name}, s.Doc.Text()
					if doc == "" {
						doc = d.Doc.Text()
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						names = append(names, id.Name)
					}
					doc = s.Doc.Text()
					if doc == "" {
						doc = d.Doc.Text()
					}
				}
				for _, name := range names {
					if name == "_" || (d.Tok != token.TYPE && !ast.IsExported(name)) {
						continue
					}
					out = append(out, symbol{name: name, file: rel, pkg: pkg, responsibility: summary(doc)})
				}
			}
		}
	}
	return out, nil
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func receiverName(field *ast.FieldList) string {
	if len(field.List) == 0 {
		return ""
	}
	text := ""
	switch t := field.List[0].Type.(type) {
	case *ast.Ident:
		text = t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			text = id.Name
		}
	}
	if text == "" {
		return ""
	}
	return "(*" + text + ")"
}

func summary(doc string) string {
	doc = strings.Join(strings.Fields(doc), " ")
	if doc == "" {
		return "—"
	}
	if end := strings.Index(doc, ". "); end >= 0 {
		doc = doc[:end+1]
	}
	return strings.ReplaceAll(doc, "|", "\\|")
}

func write(w io.Writer, symbols []symbol) error {
	if _, err := fmt.Fprintln(w, "# Generated source symbols\n\n<!-- Regenerate with `make symbols`; do not edit manually. -->\n\n| Symbol | File | Package/module | Responsibility |\n|---|---|---|---|"); err != nil {
		return err
	}
	for _, s := range symbols {
		if _, err := fmt.Fprintf(w, "| `%s` | `%s` | `%s` | %s |\n", s.name, s.file, s.pkg, s.responsibility); err != nil {
			return err
		}
	}
	return nil
}
