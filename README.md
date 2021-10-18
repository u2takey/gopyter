# gopyter

Gopyter provide a template to develop kernels with golang. Now gopyter provide two built-in kernels: starlark-go and lua-go

## Install Starlark-go

```go
go install github.com/u2takey/gopyter@latest
mkdir -p `jupyter --data-dir`/kernels/starlark-go
cp ./kernel/kernel_starlark_go.json `jupyter --data-dir`/kernels/starlark-go/kernel.json
```


## Install Lua-go

```go
go install github.com/u2takey/gopyter@latest
mkdir -p `jupyter --data-dir`/kernels/lua-go
cp ./kernel/kernel_lua_go.json `jupyter --data-dir`/kernels/lua-go/kernel.json
```


## How to Implement new Kernel 

Just implement interface KernelInterface and do RegisterKernel

```go
type KernelInterface interface {
	Init() error
	Eval(outerr OutErr, code string) (val []interface{}, err error)
	Name() string
}
```