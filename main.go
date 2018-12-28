package main

import (
	"flag"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/valyala/fasthttp"
)

var (
	ErrWrongPhase    = errors.New(`Wrong phase`)
	ErrWrongAmmoFile = errors.New(`Cannot parse ammo file`)
	ErrResponseDiff  = errors.New(`The server response is different than expected`)
)

var (
	bytesNull  = []byte(`null`)
	bytesEmpty = []byte(`<EMPTY>`)
)

type (
	Header struct {
		Key, Value []byte
	}

	Request struct {
		Skip    bool
		LineNo  int
		IsGet   bool
		URI     []byte
		Headers []Header
		Body    []byte
	}

	Response struct {
		Status int
		Body   []byte
	}

	Bullet struct {
		Request  Request
		Response Response
	}

	BenchResult struct {
		bulletIdx int
		status    int
		body      []byte
	}
)

var (
	argv struct {
		hlcupdocsPath string
		serverAddr    string
		filterReq     string
		phase         uint
		benchTime     time.Duration
		concurrent    uint
		testRun       bool
		hideFailed    bool
		allowNulls    bool
		utf8          bool
		bodyDiff      bool
		tankRps       uint
	}

	bullets []*Bullet

	emptyPOSTResponseBody = []byte(`{}`)
)

func init() {
	flag.StringVar(&argv.hlcupdocsPath, `hlcupdocs`, `./hlcupdocs/`, `path to hlcupdocs/`)
	flag.StringVar(&argv.serverAddr, `addr`, `http://127.0.0.1:80`, `test server address`)
	flag.StringVar(&argv.filterReq, `filter`, ``, `regexp for filter requests, i.e. "^458 " or "/accounts/filter/"`)
	flag.UintVar(&argv.phase, `phase`, 1, `phase number (1, 2, 3)`)
	flag.DurationVar(&argv.benchTime, `time`, 10*time.Second, `benchmark duration`)
	flag.BoolVar(&argv.testRun, `test`, false, `test run (send every query only once. ignore -time and -concurrent)`)
	flag.BoolVar(&argv.hideFailed, `hide-failed`, false, `do not print info about every failed request`)
	flag.BoolVar(&argv.allowNulls, `allow-nulls`, false, `allow null in response data`)
	flag.BoolVar(&argv.utf8, `utf8`, false, `show request & response bodies in UTF-8 human-readable format`)
	flag.BoolVar(&argv.bodyDiff, `diff`, false, `show colorful body diffs instead both variants (red - wrong, green - need)`)

	flag.UintVar(&argv.concurrent, `concurrent`, 1, `concurrent users`)
	flag.UintVar(&argv.tankRps, `tank`, 0, `run as tank: 0 -> this (rps) for benchmark duration. ignore -concurrent`)

	flag.Parse()
}

func main() {
	if err := loadData(); err != nil {
		log.Fatalln(errors.Wrap(err, `Cannot load data from `+argv.hlcupdocsPath))
	}

	fmt.Println(`bullets count:`, len(bullets))

	benchServer()
}

func benchServer() {
	var queries int64

	client := &fasthttp.Client{}

	mt := time.Now().UnixNano()

	var enough, currBullet int64

	concurrent := int(argv.concurrent)

	if argv.testRun {
		fmt.Println(`Start test run`)
		concurrent = 1
	}

	var maxReqNo int
	var muMaxReqNo sync.Mutex

	var benchResultsAll benchResult
	wg := &sync.WaitGroup{}

	if argv.tankRps == 0 {
		fmt.Printf("Start %s benchmark in %d concurrent users\n", argv.benchTime, concurrent)
		benchResultsAll = make(benchResult, concurrent)
		wg.Add(int(concurrent))
		for i := 0; i < int(concurrent); i++ {
			go pifpaf(i, &benchResultsAll, client, &enough, &queries, wg)
		}
	} else {
		currBullet = 0
		fmt.Printf("Start %s benchmark in tank mode 0->%d\n", argv.benchTime, argv.tankRps)
		delta := float64(argv.tankRps*uint(time.Second)) / float64(argv.benchTime)
		benchResultsAll = make(benchResult, 0)
		offset := 0
		pause := time.Duration(0)
		for wrk := float64(0); wrk < float64(argv.tankRps); wrk += delta {
			wg.Add(int(wrk) + 1)
			for i := 0; i < int(wrk)+1; i++ {
				benchResultsAll = append(benchResultsAll, nil)
				go func(ii int, slp time.Duration) {
					time.Sleep(slp)
					muMaxReqNo.Lock()
					if maxReqNo < ii {
						fmt.Printf("\rSending %d request", ii)
						maxReqNo = ii
					}
					muMaxReqNo.Unlock()
					pifpafTank(ii, &benchResultsAll, client, &queries, &currBullet, wg)
				}(offset, pause)
				offset++
			}
			pause += time.Second
		}
	}

	if !argv.testRun {
		time.Sleep(argv.benchTime)
		atomic.StoreInt64(&enough, 1)
	}

	wg.Wait()

	mt = (time.Now().UnixNano() - mt) / int64(time.Millisecond)
	rps := float64(queries) / (float64(mt) / 1000)
	fmt.Printf("Done. %d queries in %d ms => %.0f rps\n", queries, mt, rps)

	fmt.Println(`Check the answers...`)

	var errorsAll int64

	wg.Add(len(benchResultsAll))
	for i := 0; i < len(benchResultsAll); i++ {
		go func(i int) {
			defer wg.Done()

			var myErrors int64

			hideFailed := argv.hideFailed

			for _, benchResult := range benchResultsAll[i] {
				bullet := bullets[benchResult.bulletIdx]

				if benchResult.status != bullet.Response.Status {
					if !hideFailed {
						bodyReq, bodyRespGot, bodyRespExpect := getReqRespBodies(bullet, &benchResult)
						fmt.Printf("REQUEST  URI: %s\nREQUEST BODY: %s\n", bullet.Request.URI, bodyReq)
						fmt.Printf("STATUS GOT: %d \nSTATUS EXP: %d\n", benchResult.status, bullet.Response.Status)

						if argv.bodyDiff {
							dmp := diffmatchpatch.New()
							diffs := dmp.DiffMain(string(bodyRespGot), string(bodyRespExpect), false)
							fmt.Printf("BODIES DIFF: %s\n\n", dmp.DiffPrettyText(diffs))
						} else {
							fmt.Printf("BODY   GOT: %s\nBODY   EXP: %s\n\n", bodyRespGot, bodyRespExpect)
						}

					}
					myErrors++
				} else if (bullet.Response.Status == 200) && !equalResponseBodies(benchResult.body, bullet.Response.Body) {
					if !hideFailed {
						bodyReq, bodyRespGot, bodyRespExpect := getReqRespBodies(bullet, &benchResult)
						fmt.Printf("REQUEST  URI: %s\nREQUEST BODY: %s\n", bullet.Request.URI, bodyReq)

						if argv.bodyDiff {
							dmp := diffmatchpatch.New()
							diffs := dmp.DiffMain(string(bodyRespGot), string(bodyRespExpect), false)
							fmt.Printf("BODIES DIFF: %s\n\n", dmp.DiffPrettyText(diffs))
						} else {
							fmt.Printf("BODY   GOT: %s\nBODY   EXP: %s\n\n", bodyRespGot, bodyRespExpect)
						}
					}
					myErrors++
				}
			}

			if myErrors > 0 {
				atomic.AddInt64(&errorsAll, myErrors)
			}
		}(i)
	}

	wg.Wait()

	if errorsAll == 0 {
		fmt.Println(`All answers is OK`)
	} else {
		fmt.Printf("%d requests (%.2f%%) failed\n", errorsAll, 100*float64(errorsAll)/float64(queries))
	}
}

func getReqRespBodies(bullet *Bullet, benchResult *BenchResult) (bodyReq, bodyRespGot, bodyRespExpect []byte) {
	bodyReq = bullet.Request.Body
	bodyRespGot = benchResult.body
	bodyRespExpect = bullet.Response.Body

	if argv.utf8 {
		bodyReq = utf8MixedUnescaped(bodyReq)
		bodyRespGot = utf8MixedUnescaped(bodyRespGot)
		bodyRespExpect = utf8MixedUnescaped(bodyRespExpect)
	}

	return
}
