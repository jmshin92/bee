// Copyright 2013 bee authors
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package swaggergen

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v2"

	"github.com/astaxie/beego/utils"
	"github.com/beego/bee/generate/swaggergen/swagger"
	"github.com/beego/bee/logger"
	bu "github.com/beego/bee/utils"
)

const (
	ajson  = "application/json"
	axml   = "application/xml"
	aplain = "text/plain"
	ahtml  = "text/html"
	aform  = "multipart/form-data"
)

const (
	astTypeArray  = "array"
	astTypeObject = "object"
	astTypeMap    = "map"
)

var pkgCache map[string]struct{} //pkg:controller:function:comments comments: key:value
var controllerComments map[string]string
var importlist map[string]string
var controllerList map[string]map[string]*swagger.Item //controllername Paths items
var modelsList map[string]map[string]swagger.Schema
var rootapi swagger.Swagger
var astPkgs []*ast.Package

// refer to builtin.go
var basicTypes = map[string]string{
	"bool":       "boolean:",
	"uint":       "integer:int32",
	"uint8":      "integer:int32",
	"uint16":     "integer:int32",
	"uint32":     "integer:int32",
	"uint64":     "integer:int64",
	"int":        "integer:int64",
	"int8":       "integer:int32",
	"int16":      "integer:int32",
	"int32":      "integer:int32",
	"int64":      "integer:int64",
	"uintptr":    "integer:int64",
	"float32":    "number:float",
	"float64":    "number:double",
	"string":     "string:",
	"complex64":  "number:float",
	"complex128": "number:double",
	"byte":       "string:byte",
	"rune":       "string:byte",
	// builtin golang objects
	"time.Time":       "string:datetime",
	"json.RawMessage": "object:",
}

var stdlibObject = map[string]string{
	"&{time Time}":       "time.Time",
	"&{json RawMessage}": "json.RawMessage",
}

var httpMethods = map[string]bool{
	"GET":     true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"HEAD":    true,
	"OPTIONS": true,
}

func init() {
	pkgCache = make(map[string]struct{})
	controllerComments = make(map[string]string)
	importlist = make(map[string]string)
	controllerList = make(map[string]map[string]*swagger.Item)
	modelsList = make(map[string]map[string]swagger.Schema)
	astPkgs = make([]*ast.Package, 0)
}

// ParsePackagesFromDir parses packages from a given directory
func ParsePackagesFromDir(dirpath string) {
	c := make(chan error)

	go func() {
		filepath.Walk(dirpath, func(fpath string, fileInfo os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !fileInfo.IsDir() {
				return nil
			}

			// skip folder if it's a 'vendor' folder within dirpath or its child,
			// all 'tests' folders and dot folders wihin dirpath
			d, _ := filepath.Rel(dirpath, fpath)
			if !(d == "vendor" || strings.HasPrefix(d, "vendor"+string(os.PathSeparator))) &&
				!strings.Contains(d, "tests") &&
				!(d[0] == '.') {
				err = parsePackageFromDir(&astPkgs, fpath)
				if err != nil {
					// Send the error to through the channel and continue walking
					c <- fmt.Errorf("error while parsing directory: %s", err.Error())
					return nil
				}
			}
			return nil
		})
		close(c)
	}()

	for err := range c {
		beeLogger.Log.Warnf("%s", err)
	}
}

func parsePackageFromDir(astPkgs *[]*ast.Package, path string) error {
	fileSet := token.NewFileSet()
	folderPkgs, err := parser.ParseDir(fileSet, path, func(info os.FileInfo) bool {
		name := info.Name()
		return !info.IsDir() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".go")
	}, parser.ParseComments)
	if err != nil {
		return err
	}

	for _, v := range folderPkgs {
		*astPkgs = append(*astPkgs, v)
	}

	return nil
}

// GenerateDocs generates documentations for a given path.
func GenerateDocs(curpath string) {
	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, filepath.Join(curpath, "routers", "router.go"), nil, parser.ParseComments)
	if err != nil {
		beeLogger.Log.Fatalf("Error while parsing router.go: %s", err)
	}

	rootapi.Infos = swagger.Information{}
	rootapi.SwaggerVersion = "2.0"

	// Analyse API comments
	if f.Comments != nil {
		for _, c := range f.Comments {
			for _, s := range strings.Split(c.Text(), "\n") {
				if strings.HasPrefix(s, "@APIVersion") {
					rootapi.Infos.Version = strings.TrimSpace(s[len("@APIVersion"):])
				} else if strings.HasPrefix(s, "@Title") {
					rootapi.Infos.Title = strings.TrimSpace(s[len("@Title"):])
				} else if strings.HasPrefix(s, "@Description") {
					rootapi.Infos.Description = strings.TrimSpace(s[len("@Description"):])
				} else if strings.HasPrefix(s, "@TermsOfServiceUrl") {
					rootapi.Infos.TermsOfService = strings.TrimSpace(s[len("@TermsOfServiceUrl"):])
				} else if strings.HasPrefix(s, "@Contact") {
					rootapi.Infos.Contact.EMail = strings.TrimSpace(s[len("@Contact"):])
				} else if strings.HasPrefix(s, "@Name") {
					rootapi.Infos.Contact.Name = strings.TrimSpace(s[len("@Name"):])
				} else if strings.HasPrefix(s, "@URL") {
					rootapi.Infos.Contact.URL = strings.TrimSpace(s[len("@URL"):])
				} else if strings.HasPrefix(s, "@LicenseUrl") {
					if rootapi.Infos.License == nil {
						rootapi.Infos.License = &swagger.License{URL: strings.TrimSpace(s[len("@LicenseUrl"):])}
					} else {
						rootapi.Infos.License.URL = strings.TrimSpace(s[len("@LicenseUrl"):])
					}
				} else if strings.HasPrefix(s, "@License") {
					if rootapi.Infos.License == nil {
						rootapi.Infos.License = &swagger.License{Name: strings.TrimSpace(s[len("@License"):])}
					} else {
						rootapi.Infos.License.Name = strings.TrimSpace(s[len("@License"):])
					}
				} else if strings.HasPrefix(s, "@Schemes") {
					rootapi.Schemes = strings.Split(strings.TrimSpace(s[len("@Schemes"):]), ",")
				} else if strings.HasPrefix(s, "@Host") {
					rootapi.Host = strings.TrimSpace(s[len("@Host"):])
				} else if strings.HasPrefix(s, "@Base") {
					rootapi.BasePath = strings.TrimSpace(s[len("@Base"):])
				} else if strings.HasPrefix(s, "@SecurityDefinition") {
					if len(rootapi.SecurityDefinitions) == 0 {
						rootapi.SecurityDefinitions = make(map[string]swagger.Security)
					}
					var out swagger.Security
					p := getparams(strings.TrimSpace(s[len("@SecurityDefinition"):]))
					if len(p) < 2 {
						beeLogger.Log.Fatalf("Not enough params for security: %d\n", len(p))
					}
					out.Type = p[1]
					switch out.Type {
					case "oauth2":
						if len(p) < 6 {
							beeLogger.Log.Fatalf("Not enough params for oauth2: %d\n", len(p))
						}
						if !(p[3] == "implicit" || p[3] == "password" || p[3] == "application" || p[3] == "accessCode") {
							beeLogger.Log.Fatalf("Unknown flow type: %s. Possible values are `implicit`, `password`, `application` or `accessCode`.\n", p[1])
						}
						out.AuthorizationURL = p[2]
						out.Flow = p[3]
						if len(p)%2 != 0 {
							out.Description = strings.Trim(p[len(p)-1], `" `)
						}
						out.Scopes = make(map[string]string)
						for i := 4; i < len(p)-1; i += 2 {
							out.Scopes[p[i]] = strings.Trim(p[i+1], `" `)
						}
					case "apiKey":
						if len(p) < 4 {
							beeLogger.Log.Fatalf("Not enough params for apiKey: %d\n", len(p))
						}
						if !(p[3] == "header" || p[3] == "query") {
							beeLogger.Log.Fatalf("Unknown in type: %s. Possible values are `query` or `header`.\n", p[4])
						}
						out.Name = p[2]
						out.In = p[3]
						if len(p) > 4 {
							out.Description = strings.Trim(p[4], `" `)
						}
					case "basic":
						if len(p) > 2 {
							out.Description = strings.Trim(p[2], `" `)
						}
					default:
						beeLogger.Log.Fatalf("Unknown security type: %s. Possible values are `oauth2`, `apiKey` or `basic`.\n", p[1])
					}
					rootapi.SecurityDefinitions[p[0]] = out
				} else if strings.HasPrefix(s, "@Security") {
					if len(rootapi.Security) == 0 {
						rootapi.Security = make([]map[string][]string, 0)
					}
					rootapi.Security = append(rootapi.Security, getSecurity(s))
				}
			}
		}
	}
	// Analyse controller package
	for _, im := range f.Imports {
		pkgName := ""
		if im.Name != nil {
			pkgName = im.Name.Name
		} else {
			pkgPath := strings.Trim(im.Path.Value, "\"")
			pkgRealPath := getPackageRealPath(pkgPath)
			pkgName = getPackageRealName(pkgRealPath)
		}
		analyseControllerPkg(path.Join(curpath, "vendor"), pkgName, im.Path.Value)
	}
	for _, d := range f.Decls {
		switch specDecl := d.(type) {
		case *ast.FuncDecl:
			for _, l := range specDecl.Body.List {
				switch stmt := l.(type) {
				case *ast.AssignStmt:
					for _, l := range stmt.Rhs {
						if v, ok := l.(*ast.CallExpr); ok {
							// Analyze NewNamespace, it will return version and the subfunction
							selExpr, selOK := v.Fun.(*ast.SelectorExpr)
							if !selOK || selExpr.Sel.Name != "NewNamespace" {
								continue
							}
							baseurl := ""
							if len(rootapi.BasePath) > 0 {
								_, v = findBaseNamespace("", v)
							}
							traverseNameSpace(baseurl, v)
						}

					}
				}
			}
		}
	}
	os.Mkdir(path.Join(curpath, "swagger"), 0755)
	fd, err := os.Create(path.Join(curpath, "swagger", "swagger.json"))
	if err != nil {
		panic(err)
	}
	fdyml, err := os.Create(path.Join(curpath, "swagger", "swagger.yml"))
	if err != nil {
		panic(err)
	}
	defer fdyml.Close()
	defer fd.Close()
	dt, err := json.MarshalIndent(rootapi, "", "    ")
	dtyml, erryml := yaml.Marshal(rootapi)
	if err != nil || erryml != nil {
		panic(err)
	}
	_, err = fd.Write(dt)
	_, erryml = fdyml.Write(dtyml)
	if err != nil || erryml != nil {
		panic(err)
	}
}

func getPackageRealPath(imPath string) string {
	pkgRealPath := ""

	goPaths := bu.GetGOPATHs()
	for _, gp := range goPaths {
		gp, _ = filepath.EvalSymlinks(filepath.Join(gp, "src", imPath))
		if utils.FileExists(gp) {
			pkgRealPath = gp
			break
		}
	}
	return pkgRealPath
}

func getPackageRealName(pkgRealPath string) string {
	pkgRealName := ""

	if pkgRealPath == "" {
		return ""
	}

	fileSet := token.NewFileSet()
	f, err := parser.ParseDir(fileSet, pkgRealPath, nil, parser.ParseComments)
	if err != nil {
		return ""
	}

	for pk := range f {
		if pk == "" {
			continue
		}
		pkgRealName = pk
		break
	}
	return pkgRealName
}

func findBaseNamespace(url string, pp *ast.CallExpr) (string, *ast.CallExpr) {
	s, params := analyseNewNamespace(pp)
	curUrl := strings.Trim(url+s, "/")
	basePath := strings.Trim(rootapi.BasePath, "/")

	if basePath == curUrl {
		return url, pp
	} else if strings.HasPrefix(basePath, curUrl) {
		for _, sp := range params {
			switch pp := sp.(type) {
			case *ast.CallExpr:
				selName := pp.Fun.(*ast.SelectorExpr).Sel.String()
				if selName == "NSNamespace" {
					if url, node := findBaseNamespace(url+s, pp); node != nil {
						return url, node
					}
				}
			}
		}
	}
	return "", nil
}

func traverseNameSpace(baseURL string, nsExpr *ast.CallExpr) {
	s, params := analyseNewNamespace(nsExpr)
	if len(baseURL) == 0 && len(rootapi.BasePath) == 0 {
		rootapi.BasePath = s
	}

	for _, sp := range params {
		switch pp := sp.(type) {
		case *ast.CallExpr:
			selname := pp.Fun.(*ast.SelectorExpr).Sel.String()
			switch selname {
			case "NSNamespace":
				url := strings.Trim(pp.Args[0].(*ast.BasicLit).Value, `"`)
				traverseNameSpace(baseURL+url, pp)
			case "NSRouter":
				routeURL := strings.TrimRight(strings.Trim(pp.Args[0].(*ast.BasicLit).Value, "\""), "/")
				controllerName := analyseNSRouter(baseURL, routeURL, pp)
				if v, ok := controllerComments[controllerName]; ok {
					tag := strings.Trim(baseURL, "/")
					if len(tag) == 0 {
						tag = "/"
					}
					rootapi.Tags = append(rootapi.Tags, swagger.Tag{
						Name:        tag,
						Description: v,
					})
				}
			case "NSInclude":
				controllerName := analyseNSInclude(baseURL, pp)
				if v, ok := controllerComments[controllerName]; ok {
					rootapi.Tags = append(rootapi.Tags, swagger.Tag{
						Name:        strings.Trim(baseURL, "/"),
						Description: v,
					})
				}
			}
		}
	}
}

// analyseNewNamespace returns version and the others params
func analyseNewNamespace(ce *ast.CallExpr) (first string, others []ast.Expr) {
	for i, p := range ce.Args {
		if i == 0 {
			switch pp := p.(type) {
			case *ast.BasicLit:
				first = strings.Trim(pp.Value, `"`)
			}
			continue
		}
		others = append(others, p)
	}
	return
}

func appendController(x *ast.SelectorExpr, baseurl, routeurl string) string {
	cname := ""
	if v, ok := importlist[fmt.Sprint(x.X)]; ok {
		cname = v + x.Sel.Name
	}
	if apis, ok := controllerList[cname]; ok {
		for rt, item := range apis {
			tag := cname
			if baseurl+routeurl != "" {
				rt = baseurl + routeurl + rt
				tag = strings.Trim(baseurl, "/")
				if len(tag) == 0 {
					tag = "/"
				}
			}

			if item.Get != nil {
				item.Get.Tags = []string{tag}
			}
			if item.Post != nil {
				item.Post.Tags = []string{tag}
			}
			if item.Put != nil {
				item.Put.Tags = []string{tag}
			}
			if item.Patch != nil {
				item.Patch.Tags = []string{tag}
			}
			if item.Head != nil {
				item.Head.Tags = []string{tag}
			}
			if item.Delete != nil {
				item.Delete.Tags = []string{tag}
			}
			if item.Options != nil {
				item.Options.Tags = []string{tag}
			}
			if len(rootapi.Paths) == 0 {
				rootapi.Paths = make(map[string]*swagger.Item)
			}
			rt = urlReplace(rt)
			rootapi.Paths[rt] = item
		}
	}
	return cname
}

func analyseNSRouter(baseurl, routerurl string, ce *ast.CallExpr) string {
	var x *ast.SelectorExpr
	var p interface{} = ce.Args[1]

	if _, ok := p.(*ast.UnaryExpr); ok {
		x = p.(*ast.UnaryExpr).X.(*ast.CompositeLit).Type.(*ast.SelectorExpr)
	} else {
		beeLogger.Log.Warnf("Couldn't determine type\n")
	}
	return appendController(x, baseurl, routerurl)
}

func analyseNSInclude(baseurl string, ce *ast.CallExpr) string {
	cname := ""
	for _, p := range ce.Args {
		var x *ast.SelectorExpr
		var p1 interface{} = p
		if ident, ok := p1.(*ast.Ident); ok {
			if assign, ok := ident.Obj.Decl.(*ast.AssignStmt); ok {
				if len(assign.Rhs) > 0 {
					p1 = assign.Rhs[0].(*ast.UnaryExpr)
				}
			}
		}
		if _, ok := p1.(*ast.UnaryExpr); ok {
			x = p1.(*ast.UnaryExpr).X.(*ast.CompositeLit).Type.(*ast.SelectorExpr)
		} else {
			beeLogger.Log.Warnf("Couldn't determine type\n")
			continue
		}

		cname = appendController(x, baseurl, "")
	}
	return cname
}

func analyseControllerPkg(vendorPath, localName, pkgpath string) {
	pkgpath = strings.Trim(pkgpath, "\"")
	if isSystemPackage(pkgpath) {
		return
	}
	if pkgpath == "github.com/astaxie/beego" {
		return
	}
	if localName != "" {
		importlist[localName] = pkgpath
	} else {
		pps := strings.Split(pkgpath, "/")
		importlist[pps[len(pps)-1]] = pkgpath
	}
	gopaths := bu.GetGOPATHs()
	if len(gopaths) == 0 {
		beeLogger.Log.Fatal("GOPATH environment variable is not set or empty")
	}
	pkgRealpath := ""

	wg, _ := filepath.EvalSymlinks(filepath.Join(vendorPath, pkgpath))
	if utils.FileExists(wg) {
		pkgRealpath = wg
	} else {
		wgopath := gopaths
		for _, wg := range wgopath {
			wg, _ = filepath.EvalSymlinks(filepath.Join(wg, "src", pkgpath))
			if utils.FileExists(wg) {
				pkgRealpath = wg
				break
			}
		}
	}
	if pkgRealpath != "" {
		if _, ok := pkgCache[pkgpath]; ok {
			return
		}
		pkgCache[pkgpath] = struct{}{}
	} else {
		beeLogger.Log.Fatalf("Package '%s' does not exist in the GOPATH or vendor path", pkgpath)
	}

	fileSet := token.NewFileSet()
	astPkgs, err := parser.ParseDir(fileSet, pkgRealpath, func(info os.FileInfo) bool {
		name := info.Name()
		return !info.IsDir() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".go")
	}, parser.ParseComments)
	if err != nil {
		beeLogger.Log.Fatalf("Error while parsing dir at '%s': %s", pkgpath, err)
	}
	for _, pkg := range astPkgs {
		for _, fl := range pkg.Files {
			for _, d := range fl.Decls {
				switch specDecl := d.(type) {
				case *ast.FuncDecl:
					if specDecl.Recv != nil && len(specDecl.Recv.List) > 0 {
						if t, ok := specDecl.Recv.List[0].Type.(*ast.StarExpr); ok {
							// Parse controller method
							parserComments(fl, specDecl, fmt.Sprint(t.X), pkgpath)
						}
					}
				case *ast.GenDecl:
					if specDecl.Tok == token.TYPE {
						for _, s := range specDecl.Specs {
							switch tp := s.(*ast.TypeSpec).Type.(type) {
							case *ast.StructType:
								_ = tp.Struct
								// Parse controller definition comments
								if strings.TrimSpace(specDecl.Doc.Text()) != "" {
									controllerComments[pkgpath+s.(*ast.TypeSpec).Name.String()] = specDecl.Doc.Text()
								}
							}
						}
					}
				}
			}
		}
	}
}

func isSystemPackage(pkgpath string) bool {
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		goroot = runtime.GOROOT()
	}
	if goroot == "" {
		beeLogger.Log.Fatalf("GOROOT environment variable is not set or empty")
	}

	wg, _ := filepath.EvalSymlinks(filepath.Join(goroot, "src", "pkg", pkgpath))
	if utils.FileExists(wg) {
		return true
	}

	//TODO(zh):support go1.4
	wg, _ = filepath.EvalSymlinks(filepath.Join(goroot, "src", pkgpath))
	return utils.FileExists(wg)
}

func peekNextSplitString(ss string) (s string, spacePos int) {
	spacePos = strings.IndexFunc(ss, unicode.IsSpace)
	if spacePos < 0 {
		s = ss
		spacePos = len(ss)
	} else {
		s = strings.TrimSpace(ss[:spacePos])
	}
	return
}

// parse the func comments
func parserComments(fl *ast.File, f *ast.FuncDecl, controllerName, pkgpath string) error {
	var routerPath string
	var HTTPMethod string
	opts := swagger.Operation{
		Responses: make(map[string]swagger.Response),
	}
	funcName := f.Name.String()
	comments := f.Doc
	funcParamMap := buildParamMap(f.Type.Params)

	if fn := strings.ToUpper(funcName); httpMethods[fn] {
		HTTPMethod = fn
	}

	//TODO: resultMap := buildParamMap(f.Type.Results)
	if comments != nil && comments.List != nil {
		if len(comments.List) == 0 {
			return nil
		}
		for _, c := range comments.List {
			t := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if strings.HasPrefix(t, "@router") {
				elements := strings.TrimSpace(t[len("@router"):])
				e1 := strings.SplitN(elements, " ", 2)
				if len(e1) < 1 {
					return errors.New("you should has router infomation")
				}
				routerPath = e1[0]
				if len(e1) == 2 && e1[1] != "" {
					e1 = strings.SplitN(e1[1], " ", 2)
					HTTPMethod = strings.ToUpper(strings.Trim(e1[0], "[]"))
				} else {
					HTTPMethod = "GET"
				}
			} else if strings.HasPrefix(t, "@Title") {
				opts.OperationID = controllerName + "." + strings.TrimSpace(t[len("@Title"):])
			} else if strings.HasPrefix(t, "@Description") {
				desc := strings.TrimSpace(t[len("@Description"):])
				opts.Description += fmt.Sprintf("%s\n\n", strings.Trim(desc, "\""))
			} else if strings.HasPrefix(t, "@Summary") {
				opts.Summary = strings.TrimSpace(t[len("@Summary"):])
			} else if strings.HasPrefix(t, "@Success") {
				ss := strings.TrimSpace(t[len("@Success"):])
				rs := swagger.Response{}
				respCode, pos := peekNextSplitString(ss)
				ss = strings.TrimSpace(ss[pos:])
				respType, pos := peekNextSplitString(ss)
				if respType == "{object}" || respType == "{array}" {
					isArray := respType == "{array}"
					ss = strings.TrimSpace(ss[pos:])
					schemaName, pos := peekNextSplitString(ss)
					if schemaName == "" {
						beeLogger.Log.Fatalf("[%s.%s] Schema must follow {object} or {array}", controllerName, funcName)
					}
					if strings.HasPrefix(schemaName, "[]") {
						schemaName = schemaName[2:]
						isArray = true
					}
					schema := swagger.Schema{}
					if sType, ok := basicTypes[schemaName]; ok {
						typeFormat := strings.Split(sType, ":")
						schema.Type = typeFormat[0]
						schema.Format = typeFormat[1]
					} else {
						m, mod, realTypes := getModel(fl, schemaName)
						schema.Ref = "#/definitions/" + m
						if _, ok := modelsList[pkgpath+controllerName]; !ok {
							modelsList[pkgpath+controllerName] = make(map[string]swagger.Schema)
						}
						modelsList[pkgpath+controllerName][schemaName] = mod
						appendModels(fl, pkgpath, controllerName, realTypes)
					}
					if isArray {
						rs.Schema = &swagger.Schema{
							Type:  astTypeArray,
							Items: &schema,
						}
					} else {
						rs.Schema = &schema
					}
					rs.Description = strings.TrimSpace(ss[pos:])
				} else {
					rs.Description = strings.TrimSpace(ss)
				}
				opts.Responses[respCode] = rs
			} else if strings.HasPrefix(t, "@Param") {
				para := swagger.Parameter{}
				p := getparams(strings.TrimSpace(t[len("@Param "):]))
				if len(p) < 4 {
					beeLogger.Log.Fatal(controllerName + "_" + funcName + "'s comments @Param should have at least 4 params")
				}
				paramNames := strings.SplitN(p[0], "=>", 2)
				para.Name = paramNames[0]
				funcParamName := para.Name
				if len(paramNames) > 1 {
					funcParamName = paramNames[1]
				}
				paramType, ok := funcParamMap[funcParamName]
				if ok {
					delete(funcParamMap, funcParamName)
				}

				switch p[1] {
				case "query":
					fallthrough
				case "header":
					fallthrough
				case "path":
					fallthrough
				case "formData":
					fallthrough
				case "body":
					break
				default:
					beeLogger.Log.Warnf("[%s.%s] Unknown param location: %s. Possible values are `query`, `header`, `path`, `formData` or `body`.\n", controllerName, funcName, p[1])
				}
				para.In = p[1]
				pp := strings.Split(p[2], ".")
				typ := pp[len(pp)-1]
				if len(pp) >= 2 {
					isArray := false
					if p[1] == "body" && strings.HasPrefix(p[2], "[]") {
						p[2] = p[2][2:]
						isArray = true
					}
					m, mod, realTypes := getModel(fl, p[2])
					if isArray {
						para.Schema = &swagger.Schema{
							Type: astTypeArray,
							Items: &swagger.Schema{
								Ref: "#/definitions/" + m,
							},
						}
					} else {
						para.Schema = &swagger.Schema{
							Ref: "#/definitions/" + m,
						}
					}

					if _, ok := modelsList[pkgpath+controllerName]; !ok {
						modelsList[pkgpath+controllerName] = make(map[string]swagger.Schema)
					}
					modelsList[pkgpath+controllerName][typ] = mod
					appendModels(fl, pkgpath, controllerName, realTypes)
				} else {
					if typ == "auto" {
						typ = paramType
					}
					setParamType(&para, typ, fl, pkgpath, controllerName)
				}
				para.Required, _ = strconv.ParseBool(p[3])
				para.AllowEmptyValue = !para.Required
				paramDesc := strings.Trim(p[4], `" `)
				lines := strings.Split(paramDesc, `\n`)
				for _, line := range lines {
					para.Description = fmt.Sprintf("%s\n%s", para.Description, line)
				}

				if len(p) >= 6 {
					para.Default = str2RealType(p[5], para.Type)
				}

				if len(p) >= 7 {
					values := strings.Split(p[6], ":")
					if len(values) > 0 {
						para.Enum = make([]interface{}, 0, len(values))
					}
					for _, value := range values {
						para.Enum = append(para.Enum, value)
					}
				}

				opts.Parameters = append(opts.Parameters, para)
			} else if strings.HasPrefix(t, "@Failure") {
				rs := swagger.Response{}
				st := strings.TrimSpace(t[len("@Failure"):])
				var cd []rune
				var start bool
				for i, s := range st {
					if unicode.IsSpace(s) {
						if start {
							rs.Description = strings.TrimSpace(st[i+1:])
							break
						} else {
							continue
						}
					}
					start = true
					cd = append(cd, s)
				}
				opts.Responses[string(cd)] = rs
			} else if strings.HasPrefix(t, "@Deprecated") {
				opts.Deprecated, _ = strconv.ParseBool(strings.TrimSpace(t[len("@Deprecated"):]))
			} else if strings.HasPrefix(t, "@Accept") {
				accepts := strings.Split(strings.TrimSpace(strings.TrimSpace(t[len("@Accept"):])), ",")
				for _, a := range accepts {
					switch a {
					case "json":
						opts.Consumes = append(opts.Consumes, ajson)
						opts.Produces = append(opts.Produces, ajson)
					case "xml":
						opts.Consumes = append(opts.Consumes, axml)
						opts.Produces = append(opts.Produces, axml)
					case "plain":
						opts.Consumes = append(opts.Consumes, aplain)
						opts.Produces = append(opts.Produces, aplain)
					case "html":
						opts.Consumes = append(opts.Consumes, ahtml)
						opts.Produces = append(opts.Produces, ahtml)
					case "form":
						opts.Consumes = append(opts.Consumes, aform)
					}
				}
			} else if strings.HasPrefix(t, "@Security") {
				if len(opts.Security) == 0 {
					opts.Security = make([]map[string][]string, 0)
				}
				opts.Security = append(opts.Security, getSecurity(t))
			}
		}
	} else {
		return nil
	}

	if HTTPMethod != "" {
		//Go over function parameters which were not mapped and create swagger params for them
		for name, typ := range funcParamMap {
			para := swagger.Parameter{}
			para.Name = name
			setParamType(&para, typ, fl, pkgpath, controllerName)
			if paramInPath(name, routerPath) {
				para.In = "path"
			} else {
				para.In = "query"
			}
			opts.Parameters = append(opts.Parameters, para)
		}

		var item *swagger.Item
		if itemList, ok := controllerList[pkgpath+controllerName]; ok {
			if it, ok := itemList[routerPath]; !ok {
				item = &swagger.Item{}
			} else {
				item = it
			}
		} else {
			controllerList[pkgpath+controllerName] = make(map[string]*swagger.Item)
			item = &swagger.Item{}
		}
		for _, hm := range strings.Split(HTTPMethod, ",") {
			switch hm {
			case "GET":
				item.Get = &opts
			case "POST":
				item.Post = &opts
			case "PUT":
				item.Put = &opts
			case "PATCH":
				item.Patch = &opts
			case "DELETE":
				item.Delete = &opts
			case "HEAD":
				item.Head = &opts
			case "OPTIONS":
				item.Options = &opts
			}
		}
		controllerList[pkgpath+controllerName][routerPath] = item
	}
	return nil
}

func setParamType(para *swagger.Parameter, typ string, fl *ast.File, pkgpath, controllerName string) {
	isArray := false
	paraType := ""
	paraFormat := ""

	if strings.HasPrefix(typ, "[]") {
		typ = typ[2:]
		isArray = true
	}
	if typ == "string" || typ == "number" || typ == "integer" || typ == "boolean" ||
		typ == astTypeArray || typ == "file" {
		paraType = typ
	} else if sType, ok := basicTypes[typ]; ok {
		typeFormat := strings.Split(sType, ":")
		paraType = typeFormat[0]
		paraFormat = typeFormat[1]
	} else {
		m, mod, realTypes := getModel(fl, typ)
		para.Schema = &swagger.Schema{
			Ref: "#/definitions/" + m,
		}
		if _, ok := modelsList[pkgpath+controllerName]; !ok {
			modelsList[pkgpath+controllerName] = make(map[string]swagger.Schema)
		}
		modelsList[pkgpath+controllerName][typ] = mod
		appendModels(fl, pkgpath, controllerName, realTypes)
	}
	if isArray {
		if para.In == "body" {
			para.Schema = &swagger.Schema{
				Type: astTypeArray,
				Items: &swagger.Schema{
					Type:   paraType,
					Format: paraFormat,
				},
			}
		} else {
			para.Type = astTypeArray
			para.Items = &swagger.ParameterItems{
				Type:   paraType,
				Format: paraFormat,
			}
		}
	} else {
		para.Type = paraType
		para.Format = paraFormat
	}

}

func paramInPath(name, route string) bool {
	return strings.HasSuffix(route, ":"+name) ||
		strings.Contains(route, ":"+name+"/")
}

func getFunctionParamType(t ast.Expr) string {
	switch paramType := t.(type) {
	case *ast.Ident:
		return paramType.Name
	// case *ast.Ellipsis:
	// 	result := getFunctionParamType(paramType.Elt)
	// 	result.array = true
	// 	return result
	case *ast.ArrayType:
		return "[]" + getFunctionParamType(paramType.Elt)
	case *ast.StarExpr:
		return getFunctionParamType(paramType.X)
	case *ast.SelectorExpr:
		return getFunctionParamType(paramType.X) + "." + paramType.Sel.Name
	default:
		return ""

	}
}

func buildParamMap(list *ast.FieldList) map[string]string {
	i := 0
	result := map[string]string{}
	if list != nil {
		funcParams := list.List
		for _, fparam := range funcParams {
			param := getFunctionParamType(fparam.Type)
			var paramName string
			if len(fparam.Names) > 0 {
				paramName = fparam.Names[0].Name
			} else {
				paramName = fmt.Sprint(i)
				i++
			}
			result[paramName] = param
		}
	}
	return result
}

// analisys params return []string
// @Param	query		form	 string	true		"The email for login"
// [query form string true "The email for login"]
func getparams(str string) []string {
	var s []rune
	var j int
	var start bool
	var r []string
	var quoted int8
	for _, c := range str {
		if unicode.IsSpace(c) && quoted == 0 {
			if !start {
				continue
			} else {
				start = false
				j++
				r = append(r, string(s))
				s = make([]rune, 0)
				continue
			}
		}

		start = true
		if c == '"' {
			quoted ^= 1
			continue
		}
		s = append(s, c)
	}
	if len(s) > 0 {
		r = append(r, string(s))
	}
	return r
}

func getModel(fl *ast.File, str string) (definitionName string, m swagger.Schema, realTypes []string) {
	strs := strings.Split(str, ".")
	// strs = [packageName].[objectName]
	packageName := strs[0]
	if len(strs) == 1 {
		packageName = fl.Name.Name
	}
	objectname := strs[len(strs)-1]

	// Default all swagger schemas to object, if no other type is found
	m.Type = astTypeObject

	if _, ok := basicTypes[str]; ok {
		m.Title = objectname
	} else {
		localPkgs := make([]*ast.Package, len(astPkgs))
		copy(localPkgs, astPkgs)
		parsePackageFromFile(&localPkgs, fl)

	L:
		for _, pkg := range localPkgs {
			if packageName == pkg.Name {
				for _, fl := range pkg.Files {
					for k, d := range fl.Scope.Objects {
						if d.Kind == ast.Typ {
							if k != objectname {
								// Still searching for the right object
								continue
							}
							parseObject(d, k, &m, &realTypes, fl, localPkgs, packageName)

							// When we've found the correct object, we can stop searching
							break L
						}
					}
				}
			}
		}
	}

	if m.Title == "" {
		// Don't log when error has already been logged
		if _, found := rootapi.Definitions[str]; !found {
			beeLogger.Log.Warnf("Cannot find the object: %s", str)
		}
		m.Title = objectname
		// TODO remove when all type have been supported
	}
	if len(rootapi.Definitions) == 0 {
		rootapi.Definitions = make(map[string]swagger.Schema)
	}
	rootapi.Definitions[str] = m
	return str, m, realTypes
}

func parseObject(d *ast.Object, k string, m *swagger.Schema, realTypes *[]string, fl *ast.File, astPkgs []*ast.Package, packageName string) {
	ts, ok := d.Decl.(*ast.TypeSpec)
	if !ok {
		beeLogger.Log.Fatalf("Unknown type without TypeSec: %v", d)
	}
	// TODO support other types, such as `MapType`, `InterfaceType` etc...
	switch t := ts.Type.(type) {
	case *ast.ArrayType:
		m.Title = k
		m.Type = astTypeArray
		if isBasicType(fmt.Sprint(t.Elt)) {
			typeFormat := strings.Split(basicTypes[fmt.Sprint(t.Elt)], ":")
			m.Format = typeFormat[0]
		} else {
			objectName := packageName + "." + fmt.Sprint(t.Elt)
			if _, ok := rootapi.Definitions[objectName]; !ok {
				objectName, _, _ = getModel(fl, objectName)
			}
			m.Items = &swagger.Schema{
				Ref: "#/definitions/" + objectName,
			}
		}
	case *ast.Ident:
		parseIdent(t, k, m, astPkgs)
	case *ast.StructType:
		parseStruct(t, k, m, realTypes, astPkgs, packageName)
	}
}

// parse as enum, in the package, find out all consts with the same type
func parseIdent(st *ast.Ident, k string, m *swagger.Schema, astPkgs []*ast.Package) {
	m.Title = k
	basicType := fmt.Sprint(st)
	if object, isStdLibObject := stdlibObject[basicType]; isStdLibObject {
		basicType = object
	}
	if t, ok := basicTypes[basicType]; ok {
		typeFormat := strings.Split(t, ":")
		m.Type = typeFormat[0]
		m.Format = typeFormat[1]
	}
	enums := make(map[int]string)
	enumValues := make(map[int]interface{})
	for _, pkg := range astPkgs {
		for _, fl := range pkg.Files {
			for _, obj := range fl.Scope.Objects {
				if obj.Kind == ast.Con {
					vs, ok := obj.Decl.(*ast.ValueSpec)
					if !ok {
						beeLogger.Log.Fatalf("Unknown type without ValueSpec: %v", vs)
					}

					ti, ok := vs.Type.(*ast.Ident)
					if !ok {
						// TODO type inference, iota not support yet
						continue
					}
					// Only add the enums that are defined by the current identifier
					if ti.Name != k {
						continue
					}

					// For all names and values, aggregate them by it's position so that we can sort them later.
					for i, val := range vs.Values {
						v, ok := val.(*ast.BasicLit)
						if !ok {
							beeLogger.Log.Warnf("Unknown type without BasicLit: %v", v)
							continue
						}
						enums[int(val.Pos())] = fmt.Sprintf("%s = %s", vs.Names[i].Name, v.Value)
						switch v.Kind {
						case token.INT:
							vv, err := strconv.Atoi(v.Value)
							if err != nil {
								beeLogger.Log.Warnf("Unknown type with BasicLit to int: %v", v.Value)
								continue
							}
							enumValues[int(val.Pos())] = vv
						case token.FLOAT:
							vv, err := strconv.ParseFloat(v.Value, 64)
							if err != nil {
								beeLogger.Log.Warnf("Unknown type with BasicLit to int: %v", v.Value)
								continue
							}
							enumValues[int(val.Pos())] = vv
						default:
							enumValues[int(val.Pos())] = strings.Trim(v.Value, `"`)
						}

					}
				}
			}
		}
	}
	// Sort the enums by position
	if len(enums) > 0 {
		var keys []int
		for k := range enums {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			m.Enum = append(m.Enum, enums[k])
		}
		// Automatically use the first enum value as the example.
		m.Example = enumValues[keys[0]]
	}

}

func normalizeTypeName(packageName, typename string) string {
	if len(strings.Split(typename, " ")) > 1 {
		typename = strings.Replace(typename, " ", ".", -1)
		typename = strings.Replace(typename, "&", "", -1)
		typename = strings.Replace(typename, "{", "", -1)
		typename = strings.Replace(typename, "}", "", -1)
	} else {
		if len(strings.Split(typename, ".")) == 1 {
			typename = packageName + "." + typename
		}
	}
	return typename
}

func parseStruct(st *ast.StructType, k string, m *swagger.Schema, realTypes *[]string, astPkgs []*ast.Package, packageName string) {
	lm := &swagger.Schema{}
	refs := make([]*swagger.Schema, 0)
	if st.Fields.List != nil {
		lm.Properties = make(map[string]swagger.Propertie)
		lm.AllOf = make([]*swagger.Schema, 0)
		for _, field := range st.Fields.List {
			isSlice, realType, sType := typeAnalyser(packageName, field)
			if (isSlice && isBasicType(realType)) || sType == astTypeObject {
				realType = normalizeTypeName(packageName, realType)
			}
			*realTypes = append(*realTypes, realType)
			mp := swagger.Propertie{}
			isObject := false
			if isSlice {
				mp.Type = astTypeArray
				if t, ok := basicTypes[(strings.Replace(realType, "[]", "", -1))]; ok {
					typeFormat := strings.Split(t, ":")
					mp.Items = &swagger.Propertie{
						Type:   typeFormat[0],
						Format: typeFormat[1],
					}
				} else {
					mp.Items = &swagger.Propertie{
						Ref: "#/definitions/" + realType,
					}
				}
			} else {
				if sType == astTypeObject {
					isObject = true
					mp.Ref = "#/definitions/" + realType
				} else if isBasicType(realType) {
					typeFormat := strings.Split(sType, ":")
					mp.Type = typeFormat[0]
					mp.Format = typeFormat[1]
				} else if realType == astTypeMap {
					typeFormat := strings.Split(sType, ":")
					mp.Type = astTypeObject
					if typeFormat[0] == astTypeObject && typeFormat[1] != "" {
						// map[string]object
						valType := normalizeTypeName(packageName, typeFormat[1])
						*realTypes = append(*realTypes, valType)
						mp.AdditionalProperties = &swagger.Propertie{
							Ref: "#/definitions/" + valType,
						}
					} else {
						// map[string]basicType
						mp.AdditionalProperties = &swagger.Propertie{
							Type:   typeFormat[0],
							Format: typeFormat[1],
						}
					}
				}
			}
			if field.Names != nil {

				// set property name as field name
				var name = field.Names[0].Name

				// if no tag skip tag processing
				if field.Tag == nil {
					lm.Properties[name] = mp
					continue
				}

				var tagValues []string

				stag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))

				defaultValue := stag.Get("doc")
				if defaultValue != "" {
					r, _ := regexp.Compile(`default\((.*)\)`)
					if r.MatchString(defaultValue) {
						res := r.FindStringSubmatch(defaultValue)
						mp.Default = str2RealType(res[1], realType)

					} else {
						beeLogger.Log.Warnf("Invalid default value: %s", defaultValue)
					}
				}

				tag := stag.Get("json")
				if tag != "" {
					tagValues = strings.Split(tag, ",")
				}

				// dont add property if json tag first value is "-"
				if len(tagValues) == 0 || tagValues[0] != "-" {

					// set property name to the left most json tag value only if is not omitempty
					if len(tagValues) > 0 && tagValues[0] != "omitempty" {
						name = tagValues[0]
					}

					if thrifttag := stag.Get("thrift"); thrifttag != "" {
						ts := strings.Split(thrifttag, ",")
						if ts[0] != "" {
							name = ts[0]
						}
					}
					if required := stag.Get("required"); required != "" {
						lm.Required = append(lm.Required, name)
					}
					if desc := stag.Get("description"); desc != "" {
						mp.Description = desc
					}

					if example := stag.Get("example"); example != "" && !isObject && !isSlice {
						mp.Example = str2RealType(example, realType)
					}

					lm.Properties[name] = mp
				}
				if ignore := stag.Get("ignore"); ignore != "" {
					continue
				}
			} else {
				if sType == astTypeObject {
					ref := &swagger.Schema{
						Ref: "#/definitions/" + realType,
					}
					refs = append(refs, ref)
				}
				// only parse case of when embedded field is TypeName
				// cases of *TypeName and Interface are not handled, maybe useless for swagger spec
				tag := ""
				if field.Tag != nil {
					stag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
					tag = stag.Get("json")
				}

				if tag != "" {
					tagValues := strings.Split(tag, ",")
					if tagValues[0] == "-" {
						//if json tag is "-", omit
						continue
					} else {
						//if json tag is "something", output: something #definition/pkgname.Type
						lm.Properties[tagValues[0]] = mp
						continue
					}
				} else {
					//if no json tag, expand all fields of the type here
					nm := &swagger.Schema{}
					for _, pkg := range astPkgs {
						for _, fl := range pkg.Files {
							for nameOfObj, obj := range fl.Scope.Objects {
								if obj.Name == fmt.Sprint(field.Type) {
									parseObject(obj, nameOfObj, nm, realTypes, fl, astPkgs, pkg.Name)
								}
							}
						}
					}
					for name, p := range nm.Properties {
						lm.Properties[name] = p
					}
					continue
				}
			}
		}
	}

	if len(refs) > 0 {
		om := lm
		lm = &swagger.Schema{}
		lm.AllOf = append(refs, om)
	}
	b, _ := json.Marshal(lm)
	_ = json.Unmarshal(b, m)
	m.Title = k
}

func typeAnalyser(packageName string, f *ast.Field) (isSlice bool, realType, swaggerType string) {
	if arr, ok := f.Type.(*ast.ArrayType); ok {
		if isBasicType(fmt.Sprint(arr.Elt)) {
			return true, fmt.Sprintf("[]%v", arr.Elt), basicTypes[fmt.Sprint(arr.Elt)]
		}
		if mp, ok := arr.Elt.(*ast.MapType); ok {
			return false, fmt.Sprintf("map[%v][%v]", mp.Key, mp.Value), astTypeObject
		}
		if star, ok := arr.Elt.(*ast.StarExpr); ok {
			basicType := fmt.Sprint(star.X)
			if _, ok := star.X.(*ast.StructType); ok {
				beeLogger.Log.Warnf("Temporary structure is not supported: %s.%s", packageName, f.Names[0])
				basicType = "json.RawMessage"
			}
			if object, isStdLibObject := stdlibObject[basicType]; isStdLibObject {
				basicType = object
			}
			if k, ok := basicTypes[basicType]; ok {
				return true, fmt.Sprintf("[]%v", basicType), k
			}
			return true, basicType, astTypeObject
		}
		return true, fmt.Sprint(arr.Elt), astTypeObject
	}
	switch t := f.Type.(type) {
	case *ast.SelectorExpr:
		basicType := fmt.Sprintf("%s.%s", t.X, t.Sel.Name)
		if object, isStdLibObject := stdlibObject[basicType]; isStdLibObject {
			basicType = object
		}
		if k, ok := basicTypes[basicType]; ok {
			return false, basicType, k
		}
		return false, basicType, astTypeObject
	case *ast.StarExpr:
		basicType := fmt.Sprint(t.X)
		if _, ok := t.X.(*ast.StructType); ok {
			// Interface as Map
			beeLogger.Log.Warnf("Temporary structure is not supported: %s.%s", packageName, f.Names[0])
			basicType = "json.RawMessage"
		}
		if object, isStdLibObject := stdlibObject[basicType]; isStdLibObject {
			basicType = object
		}
		if k, ok := basicTypes[basicType]; ok {
			return false, basicType, k
		}
		return false, basicType, astTypeObject
	case *ast.MapType:
		var val string
		switch t.Value.(type) {
		case *ast.InterfaceType:
			val = "json.RawMessage"
		case *ast.StarExpr:
			val = fmt.Sprint(t.Value.(*ast.StarExpr).X)
			if object, isStdLibObject := stdlibObject[val]; isStdLibObject {
				val = object
			}
		default:
			val = fmt.Sprintf("%v", t.Value)
		}
		if isBasicType(val) {
			return false, astTypeMap, basicTypes[val]
		}
		return false, astTypeMap, astTypeObject + ":" + val
	case *ast.InterfaceType:
		// Interface as Map
		val := "json.RawMessage"
		return false, val, astTypeObject
	}
	basicType := fmt.Sprint(f.Type)
	if object, isStdLibObject := stdlibObject[basicType]; isStdLibObject {
		basicType = object
	}
	if k, ok := basicTypes[basicType]; ok {
		return false, basicType, k
	}
	return false, basicType, astTypeObject
}

func isBasicType(Type string) bool {
	if _, ok := basicTypes[Type]; ok {
		return true
	}
	return false
}

func parsePackageFromFile(localPkgs *[]*ast.Package, fl *ast.File) {
	for _, im := range fl.Imports {
		if isSystemPackage(im.Path.Value) {
			continue
		}
		imPkgPath := strings.Trim(im.Path.Value, "\"")
		imPkgRealPath := getPackageRealPath(imPkgPath)
		if imPkgRealPath == "" {
			continue
		}
		err := parsePackageFromDir(localPkgs, imPkgRealPath)
		if err != nil {
			continue
		}
	}
}

// append models
func appendModels(fl *ast.File, pkgpath, controllerName string, realTypes []string) {
	for _, realType := range realTypes {
		if realType != "" && !isBasicType(strings.TrimLeft(realType, "[]")) &&
			!strings.HasPrefix(realType, astTypeMap) && !strings.HasPrefix(realType, "&") {
			if _, ok := modelsList[pkgpath+controllerName][realType]; ok {
				continue
			}
			_, mod, newRealTypes := getModel(fl, realType)
			modelsList[pkgpath+controllerName][realType] = mod
			appendModels(fl, pkgpath, controllerName, newRealTypes)
		}
	}
}

func getSecurity(t string) (security map[string][]string) {
	security = make(map[string][]string)
	p := getparams(strings.TrimSpace(t[len("@Security"):]))
	if len(p) == 0 {
		beeLogger.Log.Fatalf("No params for security specified\n")
	}
	security[p[0]] = make([]string, 0)
	for i := 1; i < len(p); i++ {
		security[p[0]] = append(security[p[0]], p[i])
	}
	return
}

func urlReplace(src string) string {
	pt := strings.Split(src, "/")
	for i, p := range pt {
		if len(p) > 0 {
			if p[0] == ':' {
				pt[i] = "{" + p[1:] + "}"
			} else if p[0] == '?' && p[1] == ':' {
				pt[i] = "{" + p[2:] + "}"
			}

			if pt[i][0] == '{' && strings.Contains(pt[i], ":") {
				pt[i] = pt[i][:strings.Index(pt[i], ":")] + "}"
			} else if pt[i][0] == '{' && strings.Contains(pt[i], "(") {
				pt[i] = pt[i][:strings.Index(pt[i], "(")] + "}"
			}
		}
	}
	return strings.Join(pt, "/")
}

func str2RealType(s string, typ string) interface{} {
	var err error
	var ret interface{}

	switch typ {
	case "int", "int64", "int32", "int16", "int8":
		ret, err = strconv.Atoi(s)
	case "uint", "uint64", "uint32", "uint16", "uint8":
		ret, err = strconv.ParseUint(s, 10, 0)
	case "bool":
		ret, err = strconv.ParseBool(s)
	case "float64":
		ret, err = strconv.ParseFloat(s, 64)
	case "float32":
		ret, err = strconv.ParseFloat(s, 32)
	default:
		return s
	}

	if err != nil {
		beeLogger.Log.Warnf("Invalid default value type '%s': %s", typ, s)
		return s
	}

	return ret
}
