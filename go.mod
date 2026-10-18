module diskdetective

go 1.22

require (
	github.com/wailsapp/go-webview2 v1.0.0
	golang.org/x/sys v0.27.0
)

replace github.com/wailsapp/go-webview2 => ./third_party/go-webview2

replace golang.org/x/sys => ./third_party/sys
