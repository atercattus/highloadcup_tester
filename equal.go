package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/buger/jsonparser"
)

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

func utf8MixedUnescaped(b []byte) []byte {
	if len(b) == 0 {
		return bytesEmpty
	}

	unescaped, err := jsonparser.Unescape(b, nil)
	if err != nil {
		fmt.Printf("Failed to unescape: %v", err)
		return b
	}

	return unescaped
}
