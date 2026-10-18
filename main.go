// 용량 탐정 (Disk Detective): shows how much space junk files use and where.
// It only reads the disk; it never deletes anything by itself.
package main

import (
	"flag"
	"fmt"
	"os"
)

const appVersion = "1.0.0"

func main() {
	browser := flag.Bool("browser", false, "open the UI in a web browser instead of the app window")
	noOpen := flag.Bool("no-open", false, "do not open any UI, just print the address (testing)")
	port := flag.Int("port", 0, "fixed port (testing)")
	token := flag.String("token", "", "fixed token (testing)")
	flag.Parse()

	initLog()
	logf("start v%s args=%v admin=%v", appVersion, os.Args[1:], isAdmin())
	platformInit()

	app := newApp()
	srv, err := startServer(app, *port, *token)
	if err != nil {
		fatal("내부 화면 서버를 시작하지 못했어요.\n\n" + err.Error())
		return
	}
	if *noOpen {
		fmt.Println(srv.url)
		select {}
	}
	runUI(app, srv, *browser)
	logf("exit")
	os.Exit(0)
}
