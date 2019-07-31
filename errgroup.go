// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package errgroup provides synchronization, error propagation, and Context
// cancelation for groups of goroutines working on subtasks of a common task.
package errgroup

import (
	"context"
	"strings"
	"sync"
)

// Error is an error type to track multiple errors. This is used to
// accumulate errors in cases and return them as a single "error".
type Error []error

// Error implements the interface error.
func (e Error) Error() string {
	msg := make([]string, 0, len(e))
	for _, x := range e {
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
	wg        sync.WaitGroup
	cond      *sync.Cond
	fstack    []stackitem
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
			err = Error(g.errors)
		}
	}
	g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
	return
}

func (g *Group) pop() (item stackitem) {
	g.mu.Lock()
	l := len(g.fstack)
	if l != 0 {
		item = g.fstack[0]
		copy(g.fstack, g.fstack[1:])
		g.fstack[l-1] = stackitem{}
		g.fstack = g.fstack[:l-1]
	} else {
		g.flying--
		g.cond.Broadcast()
	}
	g.mu.Unlock()
	return
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
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
		g.namespace = make(map[interface{}]int)
	}
	var key interface{}
	if len(namespace) > 0 {
		key = namespace[0]
		g.namespace[key]++
	}
	g.fstack = append(g.fstack, stackitem{namespace: key, f: f})
	if g.parallel > 0 && g.flying >= g.parallel {
		g.mu.Unlock()
		return
	}
	g.flying++
	g.mu.Unlock()

	go func() {
		for {
			item := g.pop()
			if item.f == nil {
				return
			}
			if err := item.f(); err != nil {
				g.mu.Lock()
				g.errors = append(g.errors, err)
				g.mu.Unlock()
				if g.cancel != nil {
					g.cancel()
				}
			}
			if item.namespace != nil {
				g.mu.Lock()
				n := g.namespace[item.namespace]
				n--
				if n <= 0 {
					delete(g.namespace, item.namespace)
					g.cond.Broadcast()
				} else {
					g.namespace[item.namespace] = n
				}
				g.mu.Unlock()
			}
		}
	}()
}
