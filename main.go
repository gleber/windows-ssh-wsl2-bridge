package main

import (
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/Microsoft/go-winio"
)

const (
	// pipeName is the well-known path that Windows ssh.exe consults by default.
	// No SSH_AUTH_SOCK configuration is needed on the Windows side.
	pipeName = `\\.\pipe\openssh-ssh-agent`

	// wslDistro is the WSL2 distribution that hosts the SSH agent.
	wslDistro = "NixOS"

	// agentSock is the UNIX socket path inside the WSL2 distro.
	agentSock = "/run/user/1000/ssh-agent.sock"
)

func main() {
	// Write logs to a file next to the executable so they are reachable even
	// when running without a console window (-H windowsgui).
	exePath, err := os.Executable()
	if err != nil {
		exePath = "ssh-bridge"
	}
	logPath := exePath + ".log"
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		log.SetOutput(lf)
		defer lf.Close()
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ssh-bridge] ")

	// Create the Named Pipe listener.  go-winio creates the pipe with the
	// correct security descriptor so that any local user can connect, which
	// matches the behaviour of the built-in OpenSSH agent service.
	cfg := &winio.PipeConfig{
		// Allow every local user to read/write the pipe.
		SecurityDescriptor: "D:P(A;;GA;;;WD)",
		MessageMode:        false,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	}
	ln, err := winio.ListenPipe(pipeName, cfg)
	if err != nil {
		log.Fatalf("listen %s: %v", pipeName, err)
	}
	defer ln.Close()
	log.Printf("listening on %s", pipeName)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn)
	}
}

// handleConn bridges one Named Pipe connection to the WSL2 SSH agent.
func handleConn(conn io.ReadWriteCloser) {
	defer conn.Close()

	// Spawn wsl.exe which will wake the distro if it is not yet running,
	// then hand us its stdin/stdout connected to the agent socket via socat.
	cmd := exec.Command(
		"wsl.exe",
		"-d", wslDistro,
		"--",
		"socat", "STDIO", "UNIX-CONNECT:"+agentSock,
	)

	// Attach pipes so we can relay bytes manually.
	wslIn, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("StdinPipe: %v", err)
		return
	}
	wslOut, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("StdoutPipe: %v", err)
		return
	}
	// Discard stderr so it does not surface in a windowsgui process.
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		log.Printf("cmd.Start: %v", err)
		return
	}
	log.Printf("spawned wsl pid %d", cmd.Process.Pid)

	// Relay bytes in both directions concurrently.
	// Each goroutine closes the write-end it owns when done so the other
	// side receives EOF and terminates naturally.
	var wg sync.WaitGroup
	wg.Add(2)

	// Named Pipe → wsl stdin
	go func() {
		defer wg.Done()
		defer wslIn.Close()
		if _, err := io.Copy(wslIn, conn); err != nil {
			log.Printf("pipe→wsl copy: %v", err)
		}
	}()

	// wsl stdout → Named Pipe
	go func() {
		defer wg.Done()
		defer conn.Close()
		if _, err := io.Copy(conn, wslOut); err != nil {
			log.Printf("wsl→pipe copy: %v", err)
		}
	}()

	wg.Wait()

	// Reap the child.  An error here is normal (socat exits when the socket
	// is closed), so we log at debug level only.
	if err := cmd.Wait(); err != nil {
		log.Printf("cmd.Wait: %v", err)
	}
}
