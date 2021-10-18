package main

import (
	"errors"
	"fmt"
	"log"
	"strings"

	gopher_lua_lib "github.com/u2takey/gopher-lua-lib"
	thirdlib "github.com/u2takey/starlark-go-lib"
	"github.com/yuin/gopher-lua"
	sJson "go.starlark.net/lib/json"
	sMath "go.starlark.net/lib/math"
	sTime "go.starlark.net/lib/time"
	"go.starlark.net/repl"
	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

func init() {
	RegisterKernel(NewStarLarkKernel())
	RegisterKernel(NewLuaKernel())
}

type KernelInterface interface {
	FindMatch(code string) string
	Eval(outerr OutErr, code string) (val []interface{}, err error)
	Name() string
}

var KernelRegistry = map[string]KernelInterface{}

func RegisterKernel(k KernelInterface) {
	KernelRegistry[k.Name()] = k
}

func FindKernel(name string) KernelInterface {
	return KernelRegistry[name]
}

// Implementation For StarLark-Go
type StarLarkKernel struct {
	thread  *starlark.Thread
	globals starlark.StringDict
}

func NewStarLarkKernel() *StarLarkKernel {
	k := &StarLarkKernel{}
	k.thread = &starlark.Thread{Load: repl.MakeLoad()}
	k.globals = make(starlark.StringDict)

	starlark.Universe["json"] = sJson.Module
	starlark.Universe["time"] = sTime.Module
	starlark.Universe["math"] = sMath.Module
	thirdlib.InstallAllExampleModule(starlark.Universe)
	installStarLarkExtend(starlark.Universe)
	k.thread.Name = "REPL"

	resolve.AllowSet = true
	resolve.AllowGlobalReassign = true
	resolve.AllowRecursion = true
	return k
}

func installStarLarkExtend(L starlark.StringDict) {
	L["modules"] = &starlarkstruct.Module{
		Name: "modules",
		Members: starlark.StringDict{
			"all": thirdlib.ToValue(func() (ret []string) {
				for _, v := range starlark.Universe {
					if m, ok := v.(*starlarkstruct.Module); ok {
						ret = append(ret, m.Name)
					}
				}
				return
			}),
			"inspect": thirdlib.ToValue(func(a string) (ret []string) {
				if v, ok := starlark.Universe[a]; ok {
					if m, ok := v.(*starlarkstruct.Module); ok {
						for x, y := range m.Members {
							ret = append(ret, fmt.Sprintf("%s: [%s, %s]", x, y.Type(), y.String()))
						}
					}
				}
				return
			}),
		},
	}
}

func (k *StarLarkKernel) Name() string {
	return "starlark-go"
}

func (k *StarLarkKernel) FindMatch(code string) string {
	return ""
}

func (k *StarLarkKernel) Eval(outerr OutErr, code string) (val []interface{}, err error) {
	// Capture a panic from the evaluation if one occurs and store it in the `err` return parameter.
	defer func() {
		if r := recover(); r != nil {
			var ok bool
			if err, ok = r.(error); !ok {
				err = errors.New(fmt.Sprint(r))
			}
		}
	}()

	code = evalSpecialCommands(outerr, code)

	f, err := syntax.ParseCompoundStmt("<stdin>", func() ([]byte, error) {
		return []byte(strings.Trim(code, "\n") + "\n"), nil
	})
	if err != nil {
		return nil, err
	}

	defer func(prev bool) { resolve.LoadBindsGlobally = prev }(resolve.LoadBindsGlobally)
	resolve.LoadBindsGlobally = true

	if expr := soleExpr(f); expr != nil {
		// eval
		v, err := starlark.EvalExpr(k.thread, expr, k.globals)
		if err != nil {
			log.Println(err)
			return nil, err
		}

		// print
		if v != starlark.None {
			val = append(val, v.String())
		}
	} else if err := starlark.ExecREPLChunk(f, k.thread, k.globals); err != nil {
		log.Println(err)
		return nil, err
	}

	return val, nil
}

// Implementation For Lua-Go
type LuaKernel struct {
	state        *lua.LState
	bufferString string
}

func NewLuaKernel() *LuaKernel {
	k := &LuaKernel{
		state: lua.NewState(),
	}
	k.state.RegisterModule("_G", map[string]lua.LGFunction{
		"print": k.basePrint,
	})

	gopher_lua_lib.InstallAll(k.state)

	return k
}

func (k *LuaKernel) basePrint(L *lua.LState) int {
	s := strings.Builder{}
	top := L.GetTop()
	for i := 1; i <= top; i++ {
		s.WriteString(L.ToStringMeta(L.Get(i)).String())
		if i != top {
			s.WriteString("\t")
		}
	}
	k.bufferString += s.String()
	return 0
}

func (k *LuaKernel) FindMatch(code string) string {
	return ""
}

func (k *LuaKernel) Eval(outerr OutErr, code string) (val []interface{}, err error) {
	err = k.state.DoString(code)
	val = append(val, k.bufferString)
	k.bufferString = ""
	return
}

func (k *LuaKernel) Name() string {
	return "lua-go"
}
