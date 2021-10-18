package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-zeromq/zmq4"
	"go.starlark.net/syntax"
	"golang.org/x/xerrors"
)

type kernelWrapper struct {
	KernelInterface
}

// runKernel is the main entry point to start the kernel.
func runKernel(kernelName, connectionFile string) {
	k := FindKernel(kernelName)
	if k == nil {
		log.Fatal("kernel with name: " + kernelName + " not found")
	}
	kernel := kernelWrapper{k}

	// Parse the connection info.
	var connInfo ConnectionInfo

	connData, err := ioutil.ReadFile(connectionFile)
	if err != nil {
		log.Fatal(err)
	}

	if err = json.Unmarshal(connData, &connInfo); err != nil {
		log.Fatal(err)
	}

	// Set up the ZMQ sockets through which the kernel will communicate.
	sockets, err := prepareSockets(connInfo)
	if err != nil {
		log.Fatal(err)
	}

	// TODO connect all channel handlers to a WaitGroup to ensure shutdown before returning from runKernel.

	// Start up the heartbeat handler.
	startHeartbeat(sockets.HBSocket, &sync.WaitGroup{})

	// TODO gracefully shutdown the heartbeat handler on kernel shutdown by closing the chan returned by startHeartbeat.

	type msgType struct {
		Msg zmq4.Msg
		Err error
	}

	var (
		shell = make(chan msgType)
		stdin = make(chan msgType)
		ctl   = make(chan msgType)
		quit  = make(chan int)
	)

	defer close(quit)
	poll := func(msgs chan msgType, sck zmq4.Socket) {
		defer close(msgs)
		for {
			msg, err := sck.Recv()
			select {
			case msgs <- msgType{Msg: msg, Err: err}:
			case <-quit:
				return
			}
		}
	}

	go poll(shell, sockets.ShellSocket.Socket)
	go poll(stdin, sockets.StdinSocket.Socket)
	go poll(ctl, sockets.ControlSocket.Socket)

	// Start a message receiving loop.
	for {
		select {
		case v := <-shell:
			// Handle shell messages.
			if v.Err != nil {
				log.Println(v.Err)
				continue
			}

			msg, ids, err := WireMsgToComposedMsg(v.Msg.Frames, sockets.Key)
			if err != nil {
				log.Println(err)
				return
			}

			kernel.handleShellMsg(msgReceipt{msg, ids, sockets})

		case <-stdin:
			// TODO Handle stdin socket.
			continue

		case v := <-ctl:
			if v.Err != nil {
				log.Println(v.Err)
				return
			}

			msg, ids, err := WireMsgToComposedMsg(v.Msg.Frames, sockets.Key)
			if err != nil {
				log.Println(err)
				return
			}

			kernel.handleShellMsg(msgReceipt{msg, ids, sockets})
		}
	}
}

// prepareSockets sets up the ZMQ sockets through which the kernel
// will communicate.
func prepareSockets(connInfo ConnectionInfo) (SocketGroup, error) {
	// Initialize the socket group.
	var (
		sg  SocketGroup
		err error
		ctx = context.Background()
	)

	// Create the shell socket, a request-reply socket that may receive messages from multiple frontend for
	// code execution, introspection, auto-completion, etc.
	sg.ShellSocket.Socket = zmq4.NewRouter(ctx)
	sg.ShellSocket.Lock = &sync.Mutex{}

	// Create the control socket. This socket is a duplicate of the shell socket where messages on this channel
	// should jump ahead of queued messages on the shell socket.
	sg.ControlSocket.Socket = zmq4.NewRouter(ctx)
	sg.ControlSocket.Lock = &sync.Mutex{}

	// Create the stdin socket, a request-reply socket used to request user input from a front-end. This is analogous
	// to a standard input stream.
	sg.StdinSocket.Socket = zmq4.NewRouter(ctx)
	sg.StdinSocket.Lock = &sync.Mutex{}

	// Create the iopub socket, a publisher for broadcasting data like stdout/stderr output, displaying execution
	// results or errors, kernel status, etc. to connected subscribers.
	sg.IOPubSocket.Socket = zmq4.NewPub(ctx)
	sg.IOPubSocket.Lock = &sync.Mutex{}

	// Create the heartbeat socket, a request-reply socket that only allows alternating recv-send (request-reply)
	// calls. It should echo the byte strings it receives to let the requester know the kernel is still alive.
	sg.HBSocket.Socket = zmq4.NewRep(ctx)
	sg.HBSocket.Lock = &sync.Mutex{}

	// Bind the sockets.
	address := fmt.Sprintf("%v://%v:%%v", connInfo.Transport, connInfo.IP)
	err = sg.ShellSocket.Socket.Listen(fmt.Sprintf(address, connInfo.ShellPort))
	if err != nil {
		return sg, xerrors.Errorf("could not listen on shell-socket: %w", err)
	}

	err = sg.ControlSocket.Socket.Listen(fmt.Sprintf(address, connInfo.ControlPort))
	if err != nil {
		return sg, xerrors.Errorf("could not listen on control-socket: %w", err)
	}

	err = sg.StdinSocket.Socket.Listen(fmt.Sprintf(address, connInfo.StdinPort))
	if err != nil {
		return sg, xerrors.Errorf("could not listen on stdin-socket: %w", err)
	}

	err = sg.IOPubSocket.Socket.Listen(fmt.Sprintf(address, connInfo.IOPubPort))
	if err != nil {
		return sg, xerrors.Errorf("could not listen on iopub-socket: %w", err)
	}

	err = sg.HBSocket.Socket.Listen(fmt.Sprintf(address, connInfo.HBPort))
	if err != nil {
		return sg, xerrors.Errorf("could not listen on hbeat-socket: %w", err)
	}

	// Set the message signing key.
	sg.Key = []byte(connInfo.Key)

	return sg, nil
}

// handleShellMsg responds to a message on the shell ROUTER socket.
func (kernel *kernelWrapper) handleShellMsg(receipt msgReceipt) {
	// Tell the front-end that the kernel is working and when finished notify the
	// front-end that the kernel is idle again.
	if err := receipt.PublishKernelStatus(kernelBusy); err != nil {
		log.Printf("Error publishing kernel status 'busy': %v\n", err)
	}
	defer func() {
		if err := receipt.PublishKernelStatus(kernelIdle); err != nil {
			log.Printf("Error publishing kernel status 'idle': %v\n", err)
		}
	}()

	switch receipt.Msg.Header.MsgType {
	case "kernel_info_request":
		if err := sendKernelInfo(receipt); err != nil {
			log.Fatal(err)
		}
	case "complete_request":
		if err := kernel.handleCompleteRequest(receipt); err != nil {
			log.Fatal(err)
		}
	case "execute_request":
		if err := kernel.handleExecuteRequest(receipt); err != nil {
			log.Fatal(err)
		}
	case "shutdown_request":
		handleShutdownRequest(receipt)
	default:
		log.Println("Unhandled shell message: ", receipt.Msg.Header.MsgType)
	}
}

// sendKernelInfo sends a kernel_info_reply message.
func sendKernelInfo(receipt msgReceipt) error {
	return receipt.Reply("kernel_info_reply",
		kernelInfo{
			ProtocolVersion:       ProtocolVersion,
			Implementation:        "gopyter",
			ImplementationVersion: Version,
			Banner:                fmt.Sprintf("Go kernel: gopyter - v%s", Version),
			LanguageInfo: kernelLanguageInfo{
				Name:          "go+",
				Version:       runtime.Version(),
				FileExtension: ".go",
			},
			HelpLinks: []helpLink{
				{Text: "Go+", URL: "https://goplus.org/"},
				{Text: "gopyter", URL: "https://github.com/wangfenjin/gopyter"},
			},
		},
	)
}

// handleExecuteRequest runs code from an execute_request method,
// and sends the various reply messages.
func (kernel *kernelWrapper) handleExecuteRequest(receipt msgReceipt) error {

	// Extract the data from the request.
	reqcontent := receipt.Msg.Content.(map[string]interface{})
	code := reqcontent["code"].(string)
	silent := reqcontent["silent"].(bool)

	if !silent {
		ExecCounter++
	}

	// Prepare the map that will hold the reply content.
	content := make(map[string]interface{})
	content["execution_count"] = ExecCounter

	// Tell the front-end what the kernel is about to execute.
	if err := receipt.PublishExecutionInput(ExecCounter, code); err != nil {
		log.Printf("Error publishing execution input: %v\n", err)
	}

	// Redirect the standard out from the REPL.
	oldStdout := os.Stdout
	rOut, wOut, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdout = wOut

	// Redirect the standard error from the REPL.
	oldStderr := os.Stderr
	rErr, wErr, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stderr = wErr

	var writersWG sync.WaitGroup
	writersWG.Add(2)

	jupyterStdOut := JupyterStreamWriter{StreamStdout, &receipt}
	jupyterStdErr := JupyterStreamWriter{StreamStderr, &receipt}
	outerr := OutErr{&jupyterStdOut, &jupyterStdErr}

	// Forward all data written to stdout/stderr to the front-end.
	go func() {
		defer writersWG.Done()
		io.Copy(&jupyterStdOut, rOut)
	}()

	go func() {
		defer writersWG.Done()
		io.Copy(&jupyterStdErr, rErr)
	}()

	// eval
	vals, executionErr := kernel.Eval(outerr, code)

	// Close and restore the streams.
	wOut.Close()
	os.Stdout = oldStdout

	wErr.Close()
	os.Stderr = oldStderr

	// Wait for the writers to finish forwarding the data.
	writersWG.Wait()

	if executionErr == nil {
		// if the only non-nil value should be auto-rendered graphically, render it
		data := kernel.autoRenderResults(vals)

		content["status"] = "ok"
		content["user_expressions"] = make(map[string]string)

		if !silent && len(data.Data) != 0 {
			// Publish the result of the execution.
			if err := receipt.PublishExecutionResult(ExecCounter, data); err != nil {
				log.Printf("Error publishing execution result: %v\n", err)
			}
		}
	} else {
		content["status"] = "error"
		content["ename"] = "ERROR"
		content["evalue"] = executionErr.Error()
		content["traceback"] = nil

		if err := receipt.PublishExecutionError(executionErr.Error(), []string{executionErr.Error()}); err != nil {
			log.Printf("Error publishing execution error: %v\n", err)
		}
	}

	// Send the output back to the notebook.
	return receipt.Reply("execute_reply", content)
}

// LinerUI implements repl.UI interface.
type LinerUI struct {
	result []interface{}
}

// SetPrompt is required by repl.UI interface.
func (u *LinerUI) SetPrompt(prompt string) {
	// do nothiner
}

// Printf is required by repl.UI interface.
func (u *LinerUI) Printf(format string, a ...interface{}) {
	u.result = a
}

func soleExpr(f *syntax.File) syntax.Expr {
	if len(f.Stmts) == 1 {
		if stmt, ok := f.Stmts[0].(*syntax.ExprStmt); ok {
			return stmt.X
		}
	}
	return nil
}

// handleShutdownRequest sends a "shutdown" message.
func handleShutdownRequest(receipt msgReceipt) {
	content := receipt.Msg.Content.(map[string]interface{})
	restart := content["restart"].(bool)

	reply := shutdownReply{
		Restart: restart,
	}

	if err := receipt.Reply("shutdown_reply", reply); err != nil {
		log.Fatal(err)
	}

	log.Println("Shutting down in response to shutdown_request")
	os.Exit(0)
}

// startHeartbeat starts a go-routine for handling heartbeat ping messages sent over the given `hbSocket`. The `wg`'s
// `Done` method is invoked after the thread is completely shutdown. To request a shutdown the returned `shutdown` channel
// can be closed.
func startHeartbeat(hbSocket Socket, wg *sync.WaitGroup) (shutdown chan struct{}) {
	quit := make(chan struct{})

	// Start the handler that will echo any received messages back to the sender.
	wg.Add(1)
	go func() {
		defer wg.Done()

		type msgType struct {
			Msg zmq4.Msg
			Err error
		}

		msgs := make(chan msgType)

		go func() {
			defer close(msgs)
			for {
				msg, err := hbSocket.Socket.Recv()
				select {
				case msgs <- msgType{msg, err}:
				case <-quit:
					return
				}
			}
		}()

		timeout := time.NewTimer(500 * time.Second)
		defer timeout.Stop()

		for {
			timeout.Reset(500 * time.Second)
			select {
			case <-quit:
				return
			case <-timeout.C:
				continue
			case v := <-msgs:
				hbSocket.RunWithSocket(func(echo zmq4.Socket) error {
					if v.Err != nil {
						log.Fatalf("Error reading heartbeat ping bytes: %v\n", v.Err)
						return v.Err
					}

					// Send the received byte string back to let the front-end know that the kernel is alive.
					if err := echo.Send(v.Msg); err != nil {
						log.Printf("Error sending heartbeat pong bytes: %b\n", err)
						return err
					}

					return nil
				})
			}
		}
	}()

	return quit
}

// find and execute special commands in code, remove them from returned string
func evalSpecialCommands(outerr OutErr, code string) string {
	lines := strings.Split(code, "\n")
	stop := false
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) != 0 {
			switch line[0] {
			case '$':
				evalShellCommand(outerr, line)
				lines[i] = ""
			default:
				// if a line is NOT a special command,
				// stop processing special commands
				stop = true
			}
		}
		if stop {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// execute shell command. line must start with '$'
func evalShellCommand(outerr OutErr, line string) {
	args := strings.Fields(line[1:])
	if len(args) <= 0 {
		return
	}

	var writersWG sync.WaitGroup
	writersWG.Add(2)

	cmd := exec.Command(args[0], args[1:]...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(fmt.Errorf("Command.StdoutPipe() failed: %v", err))
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		panic(fmt.Errorf("Command.StderrPipe() failed: %v", err))
	}

	go func() {
		defer writersWG.Done()
		io.Copy(outerr.out, stdout)
	}()

	go func() {
		defer writersWG.Done()
		io.Copy(outerr.err, stderr)
	}()

	err = cmd.Start()
	if err != nil {
		panic(fmt.Errorf("error starting command '%s': %v", line[1:], err))
	}

	err = cmd.Wait()
	if err != nil {
		panic(fmt.Errorf("error waiting for command '%s': %v", line[1:], err))
	}

	writersWG.Wait()
}
