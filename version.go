package main

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionText string

func appVersion() string { return strings.TrimSpace(versionText) }
