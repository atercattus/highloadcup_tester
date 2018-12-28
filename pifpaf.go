package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
)

type benchResult [][]BenchResult

func pifpaf(i int, benchResultsAll *benchResult, client *fasthttp.Client, enough, queries *int64, wg *sync.WaitGroup) {
	defer wg.Done()

	var myQueries int64

	testRun := argv.testRun

	uri := []byte(argv.serverAddr)
	uriBase := len(uri)

	for atomic.LoadInt64(enough) == 0 {
		for bulletIdx, bullet := range bullets {
			uri = append(uri[:uriBase], bullet.Request.URI...)

			req := fasthttp.AcquireRequest()
			req.SetRequestURIBytes(uri)
			for _, header := range bullet.Request.Headers {
				req.Header.SetBytesKV(header.Key, header.Value)
			}
			if len(bullet.Request.Body) > 0 {
				req.SetBody(bullet.Request.Body)
			}
			if !bullet.Request.IsGet {
				req.Header.SetMethod(`POST`)
			}

			resp := fasthttp.AcquireResponse()

			oneBenchResult := BenchResult{bulletIdx: bulletIdx}

			myQueries++

			err := client.DoTimeout(req, resp, 2*time.Second)
			if err != nil {
				oneBenchResult.status = -1
				//fmt.Println(`client.DoTimeout fail:`, err)
			} else {
				oneBenchResult.status = resp.StatusCode()
				oneBenchResult.body = append(oneBenchResult.body, resp.Body()...)
			}
			(*benchResultsAll)[i] = append((*benchResultsAll)[i], oneBenchResult)

			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
		}

		if testRun {
			break
		}
	}

	atomic.AddInt64(queries, myQueries)
}

func pifpafTank(i int, benchResultsAll *benchResult, client *fasthttp.Client, queries, currBullet *int64, wg *sync.WaitGroup) {
	defer wg.Done()

	var myQueries int64

	uri := []byte(argv.serverAddr)
	uriBase := len(uri)

	bulletIdx := atomic.AddInt64(currBullet, 1)
	if int(bulletIdx) >= len(bullets) {
		return
	}

	muMaxReqNo.Lock()
	if maxReqNo < bulletIdx {
		fmt.Printf("\rSending %d request", bulletIdx)
		maxReqNo = bulletIdx
	}
	muMaxReqNo.Unlock()

	bullet := bullets[bulletIdx]
	uri = append(uri[:uriBase], bullet.Request.URI...)

	req := fasthttp.AcquireRequest()
	req.SetRequestURIBytes(uri)
	for _, header := range bullet.Request.Headers {
		req.Header.SetBytesKV(header.Key, header.Value)
	}
	if len(bullet.Request.Body) > 0 {
		req.SetBody(bullet.Request.Body)
	}
	if !bullet.Request.IsGet {
		req.Header.SetMethod(`POST`)
	}

	resp := fasthttp.AcquireResponse()

	oneBenchResult := BenchResult{bulletIdx: int(bulletIdx)}

	myQueries++

	err := client.DoTimeout(req, resp, 2*time.Second)
	if err != nil {
		oneBenchResult.status = -1
		//fmt.Println(`client.DoTimeout fail:`, err)
	} else {
		oneBenchResult.status = resp.StatusCode()
		oneBenchResult.body = append(oneBenchResult.body, resp.Body()...)
	}
	(*benchResultsAll)[i] = append((*benchResultsAll)[i], oneBenchResult)

	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	atomic.AddInt64(queries, myQueries)
}
