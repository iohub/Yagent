package main

import "embed"

//go:embed dist/bin/*
var distBinFS embed.FS

//go:embed dist/data/models.json
var distDataFS embed.FS
