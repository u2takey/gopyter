module github.com/u2takey/gopyter

go 1.16

require (
	github.com/go-zeromq/zmq4 v0.13.0
	github.com/gofrs/uuid v4.0.0+incompatible
	github.com/u2takey/gopher-lua-lib v0.0.0-20211007095423-7433523bd4af
	github.com/u2takey/starlark-go-lib v0.0.0-20211013160217-853ce1f9bd2b
	github.com/yuin/gopher-lua v0.0.0-20210529063254-f4c35e4016d9
	go.starlark.net v0.0.0-20210901212718-87f333178d59
	golang.org/x/net v0.0.0-20211013153659-ee2e9a082323 // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
	golang.org/x/sys v0.0.0-20211013075003-97ac67df715c // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
)

replace github.com/tengattack/gluasql v0.0.0-20210614064624-5de4ebec779d => github.com/u2takey/gluasql v0.0.0-20211006101730-47ba371bf9db
