// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package errgroup provides synchronization, error propagation, and Context
// cancelation for groups of goroutines working on subtasks of a common task.
package errgroup

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
)

// Error is an error type to track multiple errors. This is used to
// accumulate errors in cases and return them as a single "error".
type Error struct {
	Errors []error
}

// Error implements the interface error.
func (e *Error) Error() string {
	msg := make([]string, 0, len(e.Errors))
	for _, x := range e.Errors {
		msg = append(msg, x.Error())
	}
	return strings.Join(msg, ";")
}

// Group is a collection of goroutines working on subtasks that are part of
// the same overall task.
//
// A zero Group is valid and does not cancel on error.
type Group struct {
	cancel    func()
	singular  bool
	parallel  int
	flying    int
	mu        sync.Mutex
	cond      *sync.Cond
	fstack    [][]stackitem
	namespace map[interface{}]int
	errors    []error
}

type stackitem struct {
	namespace interface{}
	f         func() error
}

// WithContext returns a new Group and an associated Context derived from ctx.
//
// The derived Context is canceled the first time a function passed to Go
// returns a non-nil error or the first time Wait returns, whichever occurs
// first.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

// Singular sets the Wait() only returns the first occurred error.
func (g *Group) Singular() *Group {
	g.mu.Lock()
	g.singular = true
	g.mu.Unlock()
	return g
}

// Parallel sets the number of goroutines this group can running parallelly.
func (g *Group) Parallel(n int) *Group {
	g.mu.Lock()
	g.parallel = n
	g.mu.Unlock()
	return g
}

// Wait blocks until all function calls from the Go method have returned, then
// returns the first non-nil error (if any) from them.
//
// It support only wait a given namespace's functions. Details see TestNamespace.
func (g *Group) Wait(namespace ...interface{}) (err error) {
	var key interface{}
	if len(namespace) > 0 {
		key = namespace[0]
	}
	g.mu.Lock()
	for {
		if g.flying == 0 {
			break
		}
		if key != nil {
			// make Wait() can be invoked before Go()
			//lint:ignore S1005 namespace maybe a nil
			if n, _ := g.namespace[key]; n == 0 {
				break
			}
		}
		g.cond.Wait()
	}
	if len(g.errors) != 0 {
		if g.singular {
			err = g.errors[0]
		} else {
			err = &Error{g.errors}
		}
	}
	g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
	return
}

const stacklimited = 512

func (g *Group) push(item stackitem) {
	l := len(g.fstack)
	if l == 0 || len(g.fstack[l-1]) == stacklimited {
		g.fstack = append(g.fstack, make([]stackitem, 0, stacklimited))
	} else {
		l--
	}
	g.fstack[l] = append(g.fstack[l], item)
}

func (g *Group) pop() (item stackitem) {
	g.mu.Lock()
	fstack := g.fstack[0]
	l := len(fstack)
	ll := len(g.fstack)
	if l == 0 && ll > 1 {
		copy(g.fstack, g.fstack[1:])
		g.fstack[ll-1] = nil
		g.fstack = g.fstack[:ll-1]
		fstack = g.fstack[0]
		l = len(fstack)
	}
	if l != 0 {
		item = fstack[0]
		copy(fstack, fstack[1:])
		fstack[l-1] = stackitem{}
		g.fstack[0] = fstack[:l-1]
	} else {
		g.flying--
		g.cond.Broadcast()
	}
	g.mu.Unlock()
	return
}

func (g *Group) onfail(err error) {
	g.mu.Lock()
	g.errors = append(g.errors, err)
	g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
}

func (g *Group) delnamespace(namespace interface{}) {
	g.mu.Lock()
	n := g.namespace[namespace]
	n--
	if n <= 0 {
		delete(g.namespace, namespace)
		g.cond.Broadcast()
	} else {
		g.namespace[namespace] = n
	}
	g.mu.Unlock()
}

func (g *Group) loop() {
	var item stackitem

	defer func() {
		if x := recover(); x != nil {
			g.onfail(fmt.Errorf("panic: %v at\n%s", x, string(debug.Stack())))
			if item.namespace != nil {
				g.delnamespace(item.namespace)
			}
			go g.loop()
		}
	}()

	for {
		item = g.pop()
		if item.f == nil {
			return
		}
		if err := item.f(); err != nil {
			g.onfail(err)
		}
		if item.namespace != nil {
			g.delnamespace(item.namespace)
		}
	}
}

// Len return length of pending functions.
func (g *Group) Len() int {
	n := 0
	g.mu.Lock()
	l := len(g.fstack)
	if l > 1 {
		n += (l-2)*stacklimited + len(g.fstack[l-1])
	}
	if l > 0 {
		n += len(g.fstack[0])
	}
	g.mu.Unlock()
	return n
}

// NamespaceCount returns the count of pending namespaces.
func (g *Group) NamespaceCount() int {
	g.mu.Lock()
	n := len(g.namespace)
	g.mu.Unlock()
	return n
}

// Go calls the given function in a new goroutine.
// The count of goroutine is limited by Parallel().
//
// It support separated callback functions by given a namespace key and help
// us only Wait() a given namespace's callback functions.
//
// Due to keep compatible with previous api, first given namespace is valid.
//
// The first call to return a non-nil error cancels the group; its error will be
// returned by Wait.
func (g *Group) Go(f func() error, namespace ...interface{}) {
	var key interface{}
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
		g.namespace = make(map[interface{}]int)
	}
	if len(namespace) > 0 {
		key = namespace[0]
		g.namespace[key]++
	}
	g.push(stackitem{namespace: key, f: f})
	if g.parallel > 0 && g.flying >= g.parallel {
		g.mu.Unlock()
		return
	}
	g.flying++
	g.mu.Unlock()

	go g.loop()
}
