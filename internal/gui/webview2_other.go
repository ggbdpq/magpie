//go:build !windows && !nogui

package gui

import "github.com/wailsapp/wails/v3/pkg/application"

// webView2Check: only Windows' windows are WebView2.
func webView2Check(*application.App) bool { return true }

// webview2Exit: only Windows' Wails exits over WebView2 errors; elsewhere
// ErrorHandler's log is all there is to do.
func webview2Exit(err error, onShutdown func()) {}
