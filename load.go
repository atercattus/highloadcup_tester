package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"

	"github.com/pkg/errors"
)

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
			if !request.Skip {
				bullets = append(bullets, &Bullet{Request: request, Response: response})
			}
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
			rex                 *regexp.Regexp
			furi                []byte
		)

		if len(argv.filterReq) > 0 {
			fmt.Printf("...using filter %q\n", argv.filterReq)
			rex = regexp.MustCompile(argv.filterReq)
		}
		if len(argv.filterURI) > 0 {
			fmt.Printf("...using URI filter %q\n", argv.filterURI)
			furi = []byte(argv.filterURI)
		}

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

					if rex != nil {
						request.Skip = !rex.Match(line)
					}

				case stateQuery:
					if match := reQuery.FindSubmatch(line); len(match) != 3 {
						panic(errors.Wrap(ErrWrongAmmoFile, fmt.Sprintf(`Wrong query in %s line#%d: %s`, fileName, lineNo, line)))
					} else {
						request.IsGet = bytes.Equal(match[1], methodGET)
						request.URI = append([]byte{}, match[2]...)
						if furi != nil {
							request.Skip = !bytes.Contains(request.URI, furi)
						}
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
