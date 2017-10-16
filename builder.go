package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/hashicorp/go-version"
	sh "github.com/panux/encoding-sh"

	"gopkg.in/yaml.v2"
)

type rawPackageGenerator struct {
	SrcPath           string
	Packages          pkgmap
	OneShell          bool
	Version           string
	Sources           []string
	Script            []string
	BuildDependencies []string
	Builder           string
	Data              map[string]interface{}
}
type pkgmap map[string]pkg
type pkg struct {
	Dependencies []string
}

var dockerf = `
FROM panux/builder:%s-%s

RUN /scripts/install.sh %s
`

func tmplex(t string, fm template.FuncMap, d interface{}) string {
	tmpl := template.New("52")
	if fm != nil {
		tmpl.Funcs(fm)
	}
	_, err := tmpl.Parse(t)
	if err != nil {
		panic(err)
	}
	b := bytes.NewBuffer(nil)
	err = tmpl.Execute(b, d)
	if err != nil {
		panic(err)
	}
	return b.String()
}

func main() {
	var in string
	var otype string
	var ofile string
	var arch string
	flag.StringVar(&in, "i", "", "input file")
	flag.StringVar(&ofile, "o", "", "output file")
	flag.StringVar(&otype, "t", "", "output type (dockerfile/srcmk/mk)")
	flag.StringVar(&arch, "arch", "x86_64", "arch specification")
	flag.Parse()
	indat, err := ioutil.ReadFile(in)
	if err != nil {
		panic(err)
	}
	outf, err := os.OpenFile(ofile, os.O_CREATE|os.O_WRONLY, 0700)
	if err != nil {
		panic(err)
	}
	defer outf.Close()
	var pkgen rawPackageGenerator
	err = yaml.Unmarshal(indat, &pkgen)
	if err != nil {
		panic(err)
	}
	switch otype {
	case "dockerfile":
		if pkgen.Builder == "" {
			pkgen.Builder = "alpine"
		}
		_, err = fmt.Fprintf(outf, dockerf,
			pkgen.Builder,
			arch,
			strings.Join(pkgen.BuildDependencies, " "),
		)
		if err != nil {
			panic(err)
		}
	case "srcmk":
		_, err = fmt.Fprint(outf, "all: sources\n\n")
		if err != nil {
			panic(err)
		}
		srcs := make([]string, len(pkgen.Sources))
		for i, s := range pkgen.Sources {
			u, err := url.Parse(tmplex(s, nil, pkgen))
			if err != nil {
				panic(err)
			}
			_, n := filepath.Split(u.Path)
			switch u.Scheme {
			case "https":
				_, err = fmt.Fprintf(outf, "%s:\n\twget %s -O %s\n",
					n,
					u.String(),
					n,
				)
				if err != nil {
					panic(err)
				}
			case "git":
				n = strings.TrimSuffix(n, ".git")
				if ch := u.Query().Get("checkout"); ch != "" {
					u.RawQuery = ""
					_, err = fmt.Fprintf(outf, "%s:\n\tgit clone -b %s %s %s\n",
						n,
						ch,
						u.String(),
						n,
					)
				} else {
					_, err = fmt.Fprintf(outf, "%s:\n\tgit clone %s %s\n",
						n,
						u.String(),
						n,
					)
				}
				if err != nil {
					panic(err)
				}
			case "file":
				p, err := filepath.Abs(u.Path)
				if err != nil {
					panic(err)
				}
				_, err = fmt.Fprintf(outf, "%s:\n\tcp -r %s %s\n",
					n,
					p,
					n,
				)
				if err != nil {
					panic(err)
				}
			default:
				panic(fmt.Errorf("Scheme %s:// not recognized", u.Scheme))
			}
			srcs[i] = n
		}
		_, err = fmt.Fprintf(outf, "sources: %s\n",
			strings.Join(srcs, " "),
		)
		if err != nil {
			panic(err)
		}
	case "mk":
		_, err = fmt.Fprint(outf, "all: mktars\n\n")
		if err != nil {
			panic(err)
		}
		ver := version.Must(version.NewVersion(pkgen.Version)).String()
		for n, v := range pkgen.Packages {
			pkginfo := struct {
				Name         string
				Version      string
				Dependencies []string
			}{
				Name:         n,
				Version:      ver,
				Dependencies: v.Dependencies,
			}
			dat, err := sh.Encode(pkginfo)
			if err != nil {
				panic(err)
			}
			ystr := string(dat)
			_, err = fmt.Fprintf(outf, "define _%s_pkginfo = \n%s\nendef\n", strings.Replace(n, "-", "_", -1), ystr)
			if err != nil {
				panic(err)
			}
		}
		_, err = fmt.Fprint(outf, "out:\n\tmkdir out\ntars:\n\tmkdir tars\n\n\n")
		if err != nil {
			panic(err)
		}
		pkis := []string{}
		tars := []string{}
		for n := range pkgen.Packages {
			_, err = fmt.Fprintf(outf, "out/%s: out\n\tmkdir out/%s\nexport _%s_pkginfo\nout/%s/.pkginfo: out/%s\n\techo \"$$_%s_pkginfo\" > out/%s/.pkginfo\ntars/%s.tar.gz: tars script\n\ttar -cf tars/%s.tar.gz -C out/%s .\n\n", n, n, n, n, n, n, n, n, n, n)
			if err != nil {
				panic(err)
			}
			pkis = append(pkis, fmt.Sprintf("out/%s/.pkginfo", n))
			tars = append(tars, fmt.Sprintf("tars/%s.tar.gz", n))
		}
		_, err = fmt.Fprintf(outf, "pkis: %s\n\n", strings.Join(pkis, " "))
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(outf, ".ONESHELL:\nscript: pkis\n\t%s\n\n\n",
			strings.Join(
				strings.Split(
					"@echo Running build script\n"+tmplex(strings.Join(pkgen.Script, "\n"),
						map[string]interface{}{
							"make": func(dir string, args ...string) string {
								lines := make([]string, len(args))
								for i, a := range args {
									lines[i] = fmt.Sprintf("$(MAKE) -C %s %s", dir, a)
								}
								return strings.Join(lines, "\n")
							},
							"extract": func(name string, ext string) string {
								return strings.Join(
									[]string{
										fmt.Sprintf("tar -xf src/%s-%s.tar.%s", name, pkgen.Version, ext),
										fmt.Sprintf("mv %s-%s %s", name, pkgen.Version, name),
									},
									"\n")
							},
							"pkmv": func(file string, srcpkg string, destpkg string) string {
								if strings.HasSuffix(file, "/") { //cut off trailing /
									file = file[:len(file)-2]
								}
								dir, _ := filepath.Split(file)
								mv := fmt.Sprintf("mv %s %s",
									filepath.Join("out", srcpkg, file),
									filepath.Join("out", destpkg, dir),
								)
								if dir != "" {
									return strings.Join([]string{
										fmt.Sprintf("mkdir -p %s", filepath.Join("out", destpkg, dir)),
										mv,
									}, "\n")
								}
								return mv
							},
							"mvman": func(pkg string) string {
								return fmt.Sprintf("mkdir -p out/%s-man/usr/share\nmv out/%s/usr/share/man out/%s-man/usr/share/man", pkg, pkg, pkg)
							},
							"configure": func(dir string) string {
								if pkgen.Data["configure"] == nil {
									pkgen.Data["configure"] = []interface{}{}
								}
								car := pkgen.Data["configure"].([]interface{})
								ca := make([]string, len(car))
								for i, v := range car {
									ca[i] = v.(string)
								}
								return fmt.Sprintf("(cd %s && ./configure %s)", dir, strings.Join(ca, " "))
							},
							"confarch": func() string {
								return "$(shell uname -m)"
							},
						},
						pkgen,
					),
					"\n",
				),
				"\n\t",
			),
		)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(outf, "mktars: %s\n\n", strings.Join(tars, " "))
		if err != nil {
			panic(err)
		}
	}
}
