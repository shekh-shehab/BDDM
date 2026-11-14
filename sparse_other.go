//go:build !windows

package main

import "os"

func makeSparse(f *os.File) {}
