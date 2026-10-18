//go:build !windows

package main

import (
	"fmt"
	"os/exec"
)

func beforeExit() {}

func runUI(app *App, srv *Server, forceBrowser bool) {
	fmt.Println("화면 주소:", srv.url)
	_ = exec.Command("xdg-open", srv.url).Start()
	select {}
}
