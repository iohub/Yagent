package main

import "embed"

//go:embed dist/bin/*
var distBinFS embed.FS
