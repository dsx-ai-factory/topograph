// SPDX-FileCopyrightText: Copyright (c) 2024 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/mitchellh/mapstructure"
)

// Use a single instance of Validate, it caches struct info.
var validate = validator.New()

var (
	typeDuration      = reflect.TypeOf(time.Duration(5))             // nolint: gochecknoglobals
	typeTime          = reflect.TypeOf(time.Time{})                  // nolint: gochecknoglobals
	typeStringDecoder = reflect.TypeOf((*StringDecoder)(nil)).Elem() // nolint: gochecknoglobals
	typeFromStringer  = reflect.TypeOf((*FromStringer)(nil)).Elem()  // nolint: gochecknoglobals
)

// StringDecoder is used as a way for custom types (or alias types) to
// override the basic decoding function in the `decodeString`
// DecodeHook. `encoding.TextMashaller` was not used because it
// matches many Go types and would have potentially unexpected results.
// Specifying a custom decoding func should be very intentional.
type StringDecoder interface {
	DecodeString(value string) error
}

type FromStringer interface {
	FromString(str string) error
}

// Decode decodes generic map values from `input` to `output`, while providing helpful error information.
// `output` must be a pointer to a Go struct that contains `mapstructure` struct tags on fields that should
// be decoded. This function is useful when decoding values from configuration files parsed as
// `map[string]any` or component metadata as `map[string]string`.
//
// Most of the heavy lifting is handled by the mapstructure library. A custom decoder is used to handle
// decoding string values to the supported primitives.
func Decode(input any, output any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      output,
		ErrorUnused: false,
		DecodeHook:  decodeHook,
	})
	if err != nil {
		return fmt.Errorf("could not create decoder: %w", err)
	}

	if err = decoder.Decode(input); err != nil {
		return fmt.Errorf("could not decode configuration: %w", err)
	}

	if err = validate.Struct(output); err != nil {
		return fmt.Errorf("invalidation configuration: %w", err)
	}

	return nil
}

func decodeHook(
	f reflect.Type,
	t reflect.Type,
	data any) (any, error) {
	if t.Kind() == reflect.String && f.Kind() != reflect.String {
		return fmt.Sprintf("%v", data), nil
	}
	if f.Kind() == reflect.Pointer {
		f = f.Elem()
		data = reflect.ValueOf(data).Elem().Interface()
	}
	if f.Kind() != reflect.String {
		return data, nil
	}

	dataString := data.(string)

	var result any
	var decoder StringDecoder
	var from FromStringer

	if t.Implements(typeStringDecoder) {
		result = reflect.New(t.Elem()).Interface()
		decoder = result.(StringDecoder)
	} else if reflect.PointerTo(t).Implements(typeStringDecoder) {
		result = reflect.New(t).Interface()
		decoder = result.(StringDecoder)
	}

	if t.Implements(typeFromStringer) {
		result = reflect.New(t.Elem()).Interface()
		from = result.(FromStringer)
	} else if reflect.PointerTo(t).Implements(typeFromStringer) {
		result = reflect.New(t).Interface()
		from = result.(FromStringer)
	}

	if decoder != nil || from != nil {
		if dataString == "" {
			return nil, nil
		}
		var err error
		if decoder != nil {
			err = decoder.DecodeString(dataString)
		} else if from != nil {
			err = from.FromString(dataString)
		}
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", t.Name(), dataString, err)
		}

		return result, nil
	}

	switch t {
	case typeDuration:
		return DecodeDuration(dataString)
	case typeTime:
		return DecodeTime(dataString)
	}

	return decodeOther(t, data, dataString)
}

func DecodeDuration(dataString string) (time.Duration, error) {
	if val, err := strconv.Atoi(dataString); err == nil {
		return time.Duration(val) * time.Millisecond, nil
	}

	// Convert it by parsing
	d, err := time.ParseDuration(dataString)

	return d, invalidError(err, "duration", dataString)
}

func DecodeTime(dataString string) (time.Time, error) {
	// Convert it by parsing
	t, err := time.Parse(time.RFC3339Nano, dataString)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, dataString)

	return t, invalidError(err, "time", dataString)
}

func decodeOther(t reflect.Type, data any, dataString string) (any, error) {
	switch t.Kind() { // nolint: exhaustive
	case reflect.Uint:
		// Bit size 0 is the platform's uint size, so the conversion cannot overflow.
		val, err := strconv.ParseUint(dataString, 10, 0)

		return uint(val), invalidError(err, "uint", dataString)
	case reflect.Uint64:
		val, err := strconv.ParseUint(dataString, 10, 64)

		return val, invalidError(err, "uint64", dataString)
	case reflect.Uint32:
		val, err := strconv.ParseUint(dataString, 10, 32)

		return uint32(val), invalidError(err, "uint32", dataString)
	case reflect.Uint16:
		val, err := strconv.ParseUint(dataString, 10, 16)

		return uint16(val), invalidError(err, "uint16", dataString)
	case reflect.Uint8:
		val, err := strconv.ParseUint(dataString, 10, 8)

		return uint8(val), invalidError(err, "uint8", dataString)

	case reflect.Int:
		// Bit size 0 is the platform's int size, so the conversion cannot overflow.
		val, err := strconv.ParseInt(dataString, 10, 0)

		return int(val), invalidError(err, "int", dataString)
	case reflect.Int64:
		val, err := strconv.ParseInt(dataString, 10, 64)

		return val, invalidError(err, "int64", dataString)
	case reflect.Int32:
		val, err := strconv.ParseInt(dataString, 10, 32)

		return int32(val), invalidError(err, "int32", dataString)
	case reflect.Int16:
		val, err := strconv.ParseInt(dataString, 10, 16)

		return int16(val), invalidError(err, "int16", dataString)
	case reflect.Int8:
		val, err := strconv.ParseInt(dataString, 10, 8)

		return int8(val), invalidError(err, "int8", dataString)

	case reflect.Float32:
		val, err := strconv.ParseFloat(dataString, 32)

		return float32(val), invalidError(err, "float32", dataString)
	case reflect.Float64:
		val, err := strconv.ParseFloat(dataString, 64)

		return val, invalidError(err, "float64", dataString)

	case reflect.Bool:
		val, err := strconv.ParseBool(dataString)

		return val, invalidError(err, "bool", dataString)

	default:
		return data, nil
	}
}

func invalidError(err error, msg, value string) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("invalid %s %q", msg, value)
}
