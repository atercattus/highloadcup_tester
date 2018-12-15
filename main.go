package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/buger/jsonparser"
	"github.com/pkg/errors"
	"github.com/valyala/fasthttp"
	"io"
	"log"
	"math"
	"os"
	"path"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrWrongPhase    = errors.New(`Wrong phase`)
	ErrWrongAmmoFile = errors.New(`Cannot parse ammo file`)
	ErrResponseDiff  = errors.New(`The server response is different than expected`)
)

var (
	bytesNull = []byte(`null`)
)

type (
	Header struct {
		Key, Value []byte
	}

	Request struct {
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
		phase         uint
		benchTime     time.Duration
		concurrent    uint
		testRun       bool
		hideFailed    bool
		allowNulls    bool
		utf8          bool
	}

	bullets []*Bullet

	emptyPOSTResponseBody = []byte(`{}`)
)

func init() {
	flag.StringVar(&argv.hlcupdocsPath, `hlcupdocs`, `./hlcupdocs/`, `path to hlcupdocs/`)
	flag.StringVar(&argv.serverAddr, `addr`, `http://127.0.0.1:80`, `test server address`)
	flag.UintVar(&argv.phase, `phase`, 1, `phase number (1, 2, 3)`)
	flag.DurationVar(&argv.benchTime, `time`, 10*time.Second, `benchmark duration`)
	flag.UintVar(&argv.concurrent, `concurrent`, 1, `concurrent users`)
	flag.BoolVar(&argv.testRun, `test`, false, `test run (send every query only once. ignore -time and -concurrent)`)
	flag.BoolVar(&argv.hideFailed, `hide-failed`, false, `do not print info about every failed request`)
	flag.BoolVar(&argv.allowNulls, `allow-nulls`, false, `allow null in response data`)
	flag.BoolVar(&argv.utf8, `utf8`, false, `show request & response bodies in UTF-8 human-readable format`)
	flag.Parse()
}

func main() {
	if err := loadData(); err != nil {
		log.Fatalln(errors.Wrap(err, `Cannot load data from `+argv.hlcupdocsPath))
	}

	fmt.Println(`bullets count:`, len(bullets))

	benchServer()
}

func loadData() error {
	phase := int(argv.phase)

	phaseActions := [...]string{``, `get`, `post`, `get`}
	if phase <= 0 || phase >= len(phaseActions) {
		return errors.Wrap(ErrWrongPhase, strconv.Itoa(phase))
	}

	filePrefix := fmt.Sprintf(`phase_%d_%s.`, phase, phaseActions[phase])

	ammoFileName := path.Join(argv.hlcupdocsPath, `ammo`, filePrefix+`ammo`)
	answFileName := path.Join(argv.hlcupdocsPath, `answers`, filePrefix+`answ`)

	if requestChan, err := loadDataRequests(ammoFileName); err != nil {
		return errors.Wrap(err, `!loadDataRequests`)
	} else if responseChan, err := loadDataResponses(answFileName); err != nil {
		return errors.Wrap(err, `!loadDataResponses`)
	} else {
		for request := range requestChan {
			response, ok := <-responseChan
			if !ok {
				return errors.Wrap(err, `Answers is not enought`)
			}

			bullets = append(bullets, &Bullet{Request: request, Response: response})
		}
	}

	return nil
}

func loadDataRequests(fileName string) (chan Request, error) {
	fd, err := os.Open(fileName)
	if err != nil {
		return nil, errors.Wrap(err, `os.Open`)
	}

	reqChan := make(chan Request, 100)

	go func() {
		defer func() {
			fd.Close()
			close(reqChan)
		}()

		const (
			stateBlockHeader = iota
			stateQuery
			stateHeaders
			stateBody
		)

		var (
			reFirstLine         = regexp.MustCompile(`^\d+( (GET|POST):|$)`)
			reQuery             = regexp.MustCompile(`^(GET|POST) ([^\s]+) HTTP/`)
			methodGET           = []byte(`GET`)
			headerContentLength = []byte(`Content-Length`)
		)

		var (
			request  Request
			withBody bool
		)

		lineNo := 0
		state := stateBlockHeader
		rd := bufio.NewReader(fd)
		for {
			lineNo++
			if line, err := rd.ReadBytes('\n'); err != nil {
				if err == io.EOF {
					break
				}
				panic(errors.Wrap(err, fmt.Sprintf(`rd.ReadBytes in %s line#%d`, fileName, lineNo)))
			} else {
				line = bytes.TrimSpace(line)

				switch state {
				case stateBlockHeader:
					if !reFirstLine.Match(line) {
						panic(errors.Wrap(ErrWrongAmmoFile, fmt.Sprintf(`Wrong block header in %s line#%d: [%s]`, fileName, lineNo, line)))
					}
					state = stateQuery

					// подготовка значений
					withBody = false

					request.LineNo = lineNo
					request.URI = nil
					request.IsGet = true
					request.Headers = nil
					request.Body = nil

				case stateQuery:
					if match := reQuery.FindSubmatch(line); len(match) != 3 {
						panic(errors.Wrap(ErrWrongAmmoFile, fmt.Sprintf(`Wrong query in %s line#%d: %s`, fileName, lineNo, line)))
					} else {
						request.IsGet = bytes.Equal(match[1], methodGET)
						request.URI = append([]byte{}, match[2]...)
					}
					state = stateHeaders

				case stateHeaders:
					if len(line) == 0 {
						if request.IsGet || !withBody {
							reqChan <- request
							state = stateBlockHeader
						} else {
							state = stateBody
						}
					} else {
						pos := bytes.IndexByte(line, ':')
						key := line[0:pos]
						value := bytes.TrimSpace(line[pos+1:])

						request.Headers = append(request.Headers, Header{Key: key, Value: value})

						if bytes.Equal(key, headerContentLength) && !bytes.Equal(value, []byte{'0'}) { // хак учета "Content-Length: 0"
							withBody = true
						}
					}

				case stateBody:
					request.Body = append([]byte{}, line...)
					reqChan <- request
					state = stateBlockHeader
				}
			}
		}
	}()

	return reqChan, nil
}

func loadDataResponses(fileName string) (chan Response, error) {
	fd, err := os.Open(fileName)
	if err != nil {
		return nil, errors.Wrap(err, `os.Open`)
	}

	respChan := make(chan Response, 100)

	go func() {
		defer func() {
			fd.Close()
			close(respChan)
		}()

		var (
			reLine = regexp.MustCompile(`^(GET|POST)\s+([^\s]+)\s+(\d+)(\s+(.+))?$`)
		)

		lineNo := 0
		rd := bufio.NewReader(fd)
		for {
			lineNo++
			if line, err := rd.ReadBytes('\n'); err != nil {
				if err == io.EOF {
					break
				}
				panic(errors.Wrap(err, `rd.ReadBytes`))
			} else {
				line = bytes.TrimSpace(line)

				if match := reLine.FindSubmatch(line); len(match) < 4 {
					panic(errors.Wrap(ErrWrongAmmoFile, fmt.Sprintf(`Wrong format in line#%d: %s`, lineNo, line)))
				} else if status, err := strconv.ParseInt(string(match[3]), 10, 32); err != nil {
					panic(errors.Wrap(ErrWrongAmmoFile, fmt.Sprintf(`Wrong status in line#%d: %s`, lineNo, line)))
				} else {
					var response Response
					response.Status = int(status)

					if len(match[5]) > 0 {
						response.Body = append([]byte{}, match[5]...)
					} else if response.Status == 200 {
						response.Body = emptyPOSTResponseBody
					}

					respChan <- response
				}
			}
		}
	}()

	return respChan, nil
}

func benchServer() {
	var queries int64

	client := &fasthttp.Client{}

	mt := time.Now().UnixNano()

	var enought int64

	concurrent := int(argv.concurrent)

	if argv.testRun {
		fmt.Println(`Start test run`)
		concurrent = 1
	} else {
		fmt.Printf("Start %s benchmark in %d concurrent users\n", argv.benchTime, concurrent)
	}

	benchResultsAll := make([][]BenchResult, concurrent)

	var wg sync.WaitGroup

	wg.Add(int(concurrent))
	for i := 0; i < int(concurrent); i++ {
		go func(i int) {
			defer wg.Done()

			var myQueries int64

			testRun := argv.testRun

			uri := []byte(argv.serverAddr)
			uriBase := len(uri)

			for atomic.LoadInt64(&enought) == 0 {
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
					benchResultsAll[i] = append(benchResultsAll[i], oneBenchResult)

					fasthttp.ReleaseRequest(req)
					fasthttp.ReleaseResponse(resp)
				}

				if testRun {
					break
				}
			}

			atomic.AddInt64(&queries, myQueries)
		}(i)
	}

	if !argv.testRun {
		time.Sleep(argv.benchTime)
		atomic.StoreInt64(&enought, 1)
	}

	wg.Wait()

	mt = (time.Now().UnixNano() - mt) / int64(time.Millisecond)
	rps := float64(queries) / (float64(mt) / 1000)
	fmt.Printf("Done. %d queries in %d ms => %.0f rps\n", queries, mt, rps)

	fmt.Println(`Check the answers...`)

	var errorsAll int64

	wg.Add(int(concurrent))
	for i := 0; i < int(concurrent); i++ {
		go func(i int) {
			defer wg.Done()

			var myErrors int64

			hideFailed := argv.hideFailed

			for _, benchResult := range benchResultsAll[i] {
				bullet := bullets[benchResult.bulletIdx]

				if benchResult.status != bullet.Response.Status {
					if !hideFailed {
						bodyReq, bodyRespGot, bodyRespExpect := getReqRespBodies(bullet, &benchResult)
						fmt.Printf("REQUEST URI:%s BODY:%s\n", bullet.Request.URI, bodyReq)
						fmt.Printf("\tRESPONSE STATUS GOT %d != EXPECT %d. BODY GOT %s / EXPECT %s\n",
							benchResult.status, bullet.Response.Status, bodyRespGot, bodyRespExpect,
						)
					}
					myErrors++
				} else if (bullet.Response.Status == 200) && !equalResponseBodies(benchResult.body, bullet.Response.Body) {
					if !hideFailed {
						bodyReq, bodyRespGot, bodyRespExpect := getReqRespBodies(bullet, &benchResult)
						fmt.Printf("REQUEST URI:%s BODY:%s\n", bullet.Request.URI, bodyReq)
						fmt.Printf("\tRESPONSE BODY GOT %s != EXPECT %s\n", bodyRespGot, bodyRespExpect)
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

func equalResponseBodies(bodyResponse, bodyBullet []byte) bool {
	return jsEqualObjects(bodyResponse, bodyBullet)
}

func jsEqual(dataType jsonparser.ValueType, smthResponse, smthBullet []byte) bool {
	switch dataType {
	case jsonparser.Number:
		return jsEqualNumbers(smthResponse, smthBullet)
	case jsonparser.String:
		return jsEqualStrings(smthResponse, smthBullet)
	case jsonparser.Array:
		return jsEqualArrays(smthResponse, smthBullet)
	case jsonparser.Object:
		return jsEqualObjects(smthResponse, smthBullet)
	case jsonparser.Null:
		if !argv.allowNulls {
			return false
		}
		return bytes.Equal(smthResponse, bytesNull) && bytes.Equal(smthResponse, smthBullet)
	default:
		// не поддерживаемый тип
		return false
	}
}

func jsEqualObjects(objResponse, objBullet []byte) bool {
	return nil == jsonparser.ObjectEach(
		objBullet,
		func(key []byte, value []byte, dataType jsonparser.ValueType, offset int) error {

			valueResponse, dataTypeResponse, _, err := jsonparser.Get(objResponse, string(key))
			if err != nil {
				return err
			} else if dataType != dataTypeResponse {
				return ErrResponseDiff
			} else if !jsEqual(dataType, valueResponse, value) {
				return ErrResponseDiff
			}

			return nil
		},
	)
}

func jsEqualNumbers(numberResponse, numberBullet []byte) bool {
	if numBullet, err := strconv.ParseFloat(string(numberBullet), 64); err != nil {
		return false
	} else if numResponse, err := strconv.ParseFloat(string(numberResponse), 64); err != nil {
		return false
	} else {
		return math.Abs(numBullet-numResponse) < 1e-5
	}
}

func jsEqualStrings(stringResponse, stringBullet []byte) bool {
	return bytes.Equal(stringBullet, stringResponse) || bytes.Equal(utf8Unescaped(stringBullet), utf8Unescaped(stringResponse))
}

func jsEqualArrays(arrayResponse, arrayBullet []byte) bool {
	var err error

	type arrayItem struct {
		dataType jsonparser.ValueType
		value    []byte
	}

	var itemsResponse, itemsBullet []arrayItem

	_, err = jsonparser.ArrayEach(
		arrayBullet,
		func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
			if err != nil {
				return
			}
			itemsResponse = append(itemsResponse, arrayItem{dataType: dataType, value: value})
		},
	)
	if err != nil {
		return false
	}

	_, err = jsonparser.ArrayEach(
		arrayResponse,
		func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
			if err != nil {
				return
			}
			itemsBullet = append(itemsBullet, arrayItem{dataType: dataType, value: value})
		},
	)
	if err != nil {
		return false
	}

	if len(itemsResponse) != len(itemsBullet) {
		return false
	}

	for i, itemBullet := range itemsBullet {
		if itemBullet.dataType != itemsResponse[i].dataType {
			return false
		} else if !jsEqual(itemBullet.dataType, itemsResponse[i].value, itemBullet.value) {
			return false
		}
	}

	return true
}

// хак для перевода экранированных строк вида "\u1234\u5678" в нормальный юникод
func utf8Unescaped(b []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte('"')
	buf.Write(b)
	buf.WriteByte('"')

	var s string
	json.Unmarshal(buf.Bytes(), &s)

	return []byte(s)
}

func getReqRespBodies(bullet *Bullet, benchResult *BenchResult) (bodyReq, bodyRespGot, bodyRespExpect []byte) {
	bodyReq = bullet.Request.Body
	bodyRespGot = benchResult.body
	bodyRespExpect = bullet.Response.Body

	if argv.utf8 {
		bodyReq = utf8Unescaped(bodyReq)
		bodyRespGot = utf8Unescaped(bodyRespGot)
		bodyRespExpect = utf8Unescaped(bodyRespExpect)
	}

	return
}
