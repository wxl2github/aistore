// Package tassert provides common asserts for tests
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package tassert

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
)

var (
	fatalities = make(map[string]struct{})
	mu         sync.Mutex
)

func CheckFatal(tb testing.TB, err error) {
	if err == nil {
		return
	}
	mu.Lock()
	if _, ok := fatalities[tb.Name()]; ok {
		mu.Unlock()
		fmt.Printf("--- %s: duplicate CheckFatal\n", tb.Name()) // see #1057
		runtime.Goexit()
	} else {
		fatalities[tb.Name()] = struct{}{}
		mu.Unlock()
		debug.PrintStack()
		tb.Fatal(err.Error())
	}
}

func CheckError(tb testing.TB, err error) {
	if err != nil {
		debug.PrintStack()
		tb.Error(err.Error())
	}
}

func Fatalf(tb testing.TB, cond bool, msg string, args ...interface{}) {
	if !cond {
		debug.PrintStack()
		tb.Fatalf(msg, args...)
	}
}

func Errorf(tb testing.TB, cond bool, msg string, args ...interface{}) {
	if !cond {
		debug.PrintStack()
		tb.Errorf(msg, args...)
	}
}

// TODO: Make this a range over `errCh` post closing it ?
func SelectErr(tb testing.TB, errCh chan error, verb string, errIsFatal bool) {
	if num := len(errCh); num > 0 {
		err := <-errCh
		f := tb.Errorf
		if errIsFatal {
			f = tb.Fatalf
		}
		if num > 1 {
			f("Failed to %s %d objects, e.g. error:\n%v", verb, num, err)
		} else {
			f("Failed to %s object: %v", verb, err)
		}
	}
}
