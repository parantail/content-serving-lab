//go:build linux

package main

import (
	"github.com/parantail/content-serving-lab/internal/e2"
	"github.com/parantail/content-serving-lab/internal/e2magick"
)

func main() { e2.WorkerMain(e2magick.New()) }
