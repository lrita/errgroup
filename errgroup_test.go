// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package errgroup_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lrita/errgroup"
)

var (
	Web   = fakeSearch("web")
	Image = fakeSearch("image")
	Video = fakeSearch("video")
)

type Result string
type Search func(ctx context.Context, query string) (Result, error)

func fakeSearch(kind string) Search {
	return func(_ context.Context, query string) (Result, error) {
		return Result(fmt.Sprintf("%s result for %q", kind, query)), nil
	}
}

// JustErrors illustrates the use of a Group in place of a sync.WaitGroup to
// simplify goroutine counting and error handling. This example is derived from
// the sync.WaitGroup example at https://golang.org/pkg/sync/#example_WaitGroup.
func ExampleGroup_justErrors() {
	var g errgroup.Group
	var urls = []string{
		"http://www.golang.org/",
		"http://www.google.com/",
		"http://www.somestupidname.com/",
	}
	for _, url := range urls {
		// Launch a goroutine to fetch the URL.
		url := url // https://golang.org/doc/faq#closures_and_goroutines
		g.Go(func() error {
			// Fetch the URL.
			resp, err := http.Get(url)
			if err == nil {
				resp.Body.Close()
			}
			return err
		})
	}
	// Wait for all HTTP fetches to complete.
	if err := g.Wait(); err == nil {
		fmt.Println("Successfully fetched all URLs.")
	}
}

// Parallel illustrates the use of a Group for synchronizing a simple parallel
// task: the "Google Search 2.0" function from
// https://talks.golang.org/2012/concurrency.slide#46, augmented with a Context
// and error-handling.
func ExampleGroup_parallel() {
	Google := func(ctx context.Context, query string) ([]Result, error) {
		g, ctx := errgroup.WithContext(ctx)

		searches := []Search{Web, Image, Video}
		results := make([]Result, len(searches))
		for i, search := range searches {
			i, search := i, search // https://golang.org/doc/faq#closures_and_goroutines
			g.Go(func() error {
				result, err := search(ctx, query)
				if err == nil {
					results[i] = result
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return results, nil
	}

	results, err := Google(context.Background(), "golang")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	for _, result := range results {
		fmt.Println(result)
	}

	// Output:
	// web result for "golang"
	// image result for "golang"
	// video result for "golang"
}

func TestZeroGroup(t *testing.T) {
	err1 := errors.New("errgroup_test: 1")
	err2 := errors.New("errgroup_test: 2")

	cases := []struct {
		errs []error
	}{
		{errs: []error{}},
		{errs: []error{nil}},
		{errs: []error{err1}},
		{errs: []error{err1, nil}},
		{errs: []error{err1, nil, err2}},
	}

	for _, tc := range cases {
		var g errgroup.Group
		var firstErr error
		g.Singular().Parallel(1)
		for i, err := range tc.errs {
			err := err
			g.Go(func() error { return err })

			if firstErr == nil && err != nil {
				firstErr = err
			}

			if gErr := g.Wait(); gErr != firstErr {
				t.Errorf("after %T.Go(func() error { return err }) for err in %v\n"+
					"g.Wait() = %v; want %v",
					&g, tc.errs[:i+1], err, firstErr)
			}
		}
	}
}

func TestNamespace(t *testing.T) {
	var (
		eg  errgroup.Group
		ch0 = make(chan struct{})
		ch1 = make(chan struct{})
		ch2 = make(chan struct{})
		ch3 = make(chan struct{})
	)

	eg.Go(func() error { return nil }, 0)
	eg.Go(func() error { return nil }, 0)
	eg.Go(func() error { <-ch0; return nil }, 1)

	go func() { eg.Wait(0); close(ch1) }()
	go func() { eg.Wait(); close(ch2) }()
	go func() { eg.Wait(1); close(ch3) }()

	select {
	case <-ch1:
	case <-time.After(10 * time.Millisecond):
		t.Fatal("eg.Wait(0) timeout")
	}
	select {
	case <-ch2:
		t.Fatal("eg.Wait() should timeout")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-ch3:
		t.Fatal("eg.Wait(1) should timeout")
	case <-time.After(10 * time.Millisecond):
	}

	close(ch0)

	select {
	case <-ch2:
	case <-time.After(10 * time.Millisecond):
		t.Fatal("eg.Wait() timeout")
	}
	select {
	case <-ch3:
	case <-time.After(10 * time.Millisecond):
		t.Fatal("eg.Wait(1) timeout")
	}
}

func TestWithContext(t *testing.T) {
	errDoom := errors.New("group_test: doomed")

	cases := []struct {
		errs []error
		want error
	}{
		{want: nil},
		{errs: []error{nil}, want: nil},
		{errs: []error{errDoom}, want: errDoom},
		{errs: []error{errDoom, nil}, want: errDoom},
	}

	for _, tc := range cases {
		g, ctx := errgroup.WithContext(context.Background())
		g.Singular()
		for _, err := range tc.errs {
			err := err
			g.Go(func() error { return err })
		}

		if err := g.Wait(); err != tc.want {
			t.Errorf("after %T.Go(func() error { return err }) for err in %v\n"+
				"g.Wait() = %v; want %v",
				g, tc.errs, err, tc.want)
		}

		canceled := false
		select {
		case <-ctx.Done():
			canceled = true
		default:
		}
		if !canceled {
			t.Errorf("after %T.Go(func() error { return err }) for err in %v\n"+
				"ctx.Done() was not closed",
				g, tc.errs)
		}
	}
}

func TestWithMultiErrorReturns(t *testing.T) {
	for _, n := range []int{1, 2, 5, 10, 50} {
		var g errgroup.Group
		for i := 0; i < n; i++ {
			x := i
			g.Go(func() error {
				return fmt.Errorf("[%d]", x)
			})
		}
		if err := g.Wait(); err == nil {
			t.Error("expect got an error")
		} else {
			msg := err.Error()
			for i := 0; i < n; i++ {
				token := fmt.Sprintf("[%d]", i)
				if !strings.Contains(msg, token) {
					t.Errorf("missing msg token %q %q", token, msg)
				}
			}
		}
	}
}

func TestParallel(t *testing.T) {
	cases := []struct {
		n        int
		parallel int
	}{
		{10, 1},
		{20, 2},
		{50, 5},
		{100, 10},
		{500, 50},
	}
	for _, v := range cases {
		var (
			parallel int32
			g        errgroup.Group
		)
		g.Parallel(v.parallel)
		for i := 0; i < v.n; i++ {
			g.Go(func() error {
				x := atomic.AddInt32(&parallel, 1)
				if x < 0 || x > int32(v.parallel) {
					return fmt.Errorf("the parallel(%v) expect(%v)",
						x, v.parallel)
				}
				time.Sleep(1e7)
				x = atomic.AddInt32(&parallel, -1)
				if x < 0 || x > int32(v.parallel) {
					return fmt.Errorf("the parallel(%v) expect(%v)",
						x, v.parallel)
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			t.Errorf("expect unexpect %v", err)
		}
	}
}

func TestNoLeakage(t *testing.T) {
	f := func() error { time.Sleep(time.Minute); return nil }
	for j, v := range []int{1 << 10, 1 << 15, 1 << 20} {
		var eg errgroup.Group
		eg.Parallel(1)
		for i := 0; i < v; i++ {
			eg.Go(f, i)
		}
		time.Sleep(time.Second)
		if eg.Len() != v-1 {
			t.Errorf("%d eg.Len() got %d, expect %d", j, eg.Len(), v-1)
		}
		if eg.NamespaceCount() != v {
			t.Errorf("%d eg.NamespaceCount() got %d, expect %d",
				j, eg.NamespaceCount(), v-1)
		}
	}
}

// panic: runtime error: comparing uncomparable type errgroup.Error
func TestComparableError(t *testing.T) {
	err := error(&errgroup.Error{[]error{fmt.Errorf("xxx:%v", 1)}})
	//lint:ignore SA4000 must check this
	if err != err {
		t.Fatal("should equal")
	}
}

func BenchmarkErrGroupSerial(b *testing.B) {
	var (
		f  = func() error { _ = b.N; return nil }
		eg errgroup.Group
	)
	for i := 0; i < b.N; i++ {
		eg.Go(f)
		eg.Wait()
	}
}

func BenchmarkErrGroupParallel(b *testing.B) {
	var (
		f  = func() error { _ = b.N; return nil }
		eg errgroup.Group
	)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			eg.Go(f)
		}
	})
}

func BenchmarkErrGroupParallelWithNamespace(b *testing.B) {
	var (
		f  = func() error { _ = b.N; return nil }
		eg errgroup.Group
	)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			eg.Go(f, pb)
		}
	})
}

func BenchmarkGoWithManyPending(b *testing.B) {
	var (
		f  = func() error { time.Sleep(time.Minute); return nil }
		eg errgroup.Group
	)
	eg.Parallel(1)
	for i := 0; i < b.N; i++ {
		eg.Go(f, b)
	}
}
