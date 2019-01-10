# errgroup

[![Build Status](https://travis-ci.org/lrita/errgroup.svg?branch=master)](https://travis-ci.org/lrita/errgroup) [![GoDoc](https://godoc.org/github.com/lrita/errgroup?status.png)](https://godoc.org/github.com/lrita/errgroup) [![codecov](https://codecov.io/gh/lrita/errgroup/branch/master/graph/badge.svg)](https://codecov.io/gh/lrita/errgroup)

This repo is forked from [golang.org/x/sync/errgroup](https://godoc.org/golang.org/x/sync/errgroup) and add more feature.

Usually, we can parallel invoke some api in our business logic. But some api providers limited the api concurrents by id or authorize.

example gist:
```go
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

// Parallel illustrates the use of a Group for synchronizing a simple parallel
// task: the "Google Search 2.0" function from
// https://talks.golang.org/2012/concurrency.slide#46, augmented with a Context
// and error-handling.
func ExampleGroup_parallel() {
	Google := func(ctx context.Context, query string) ([]Result, error) {
		g, ctx := errgroup.WithContext(ctx)
		g.Parallel(2) // limit
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
```
