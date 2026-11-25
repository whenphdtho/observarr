package adapter

import (
	"encoding/json"
	"errors"
)

// DecodeObject reads a JSON object (or null) from d, handing the value of each key in fields to its function and
// skipping every other value without keeping it.
func DecodeObject(d *json.Decoder, fields map[string]func(*json.Decoder) error) error {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, _ := key.(string)
		if f, ok := fields[name]; ok {
			err = f(d)
		} else {
			err = skipValue(d)
		}
		if err != nil {
			return err
		}
	}
	_, err = d.Token() // the closing brace
	return err
}

// CountArray reads a JSON array (or null) from d, handing its first max elements, decoded as T, to each and
// skipping the rest without keeping them. It returns how many elements there were. Unlike DecodeArray it reads to
// the end of the array, for a list that must be counted.
func CountArray[T any](d *json.Decoder, max int, each func(T)) (int, error) {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return 0, err
	}
	if tok != json.Delim('[') {
		return 0, errors.New("not a JSON array")
	}
	n := 0
	for ; d.More(); n++ {
		if n >= max {
			if err := skipValue(d); err != nil {
				return n, err
			}
			continue
		}
		var v T
		if err := d.Decode(&v); err != nil {
			return n, err
		}
		each(v)
	}
	_, err = d.Token() // the closing bracket
	return n, err
}

// skipValue reads one JSON value from d token by token, keeping nothing.
func skipValue(d *json.Decoder) error {
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}
