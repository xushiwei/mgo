// gobson - BSON library for Go.
// 
// Copyright (c) 2010-2011 - Gustavo Niemeyer <gustavo@niemeyer.net>
// 
// All rights reserved.
// 
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
// 
//     * Redistributions of source code must retain the above copyright notice,
//       this list of conditions and the following disclaimer.
//     * Redistributions in binary form must reproduce the above copyright notice,
//       this list of conditions and the following disclaimer in the documentation
//       and/or other materials provided with the distribution.
//     * Neither the name of the copyright holder nor the names of its
//       contributors may be used to endorse or promote products derived from
//       this software without specific prior written permission.
// 
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR
// CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package bson

import (
	"reflect"
	"math"
	"fmt"
)

type decoder struct {
	in []byte
	i  int
}


// --------------------------------------------------------------------------
// Some helper functions.

func corrupted() {
	panic("Document is corrupted")
}

func zeroNilPtr(v reflect.Value) (changed bool) {
	if v.Kind() == reflect.Ptr && v.IsNil() {
		v.Set(reflect.New(v.Type().Elem()))
		return true
	}
	return false
}

func settableValueOf(i interface{}) reflect.Value {
	v := reflect.ValueOf(i)
	sv := reflect.New(v.Type()).Elem()
	sv.Set(v)
	return sv
}

// --------------------------------------------------------------------------
// Unmarshaling of documents.

func (d *decoder) readDocTo(out reflect.Value) {
	zeroNilPtr(out)

	if setter, ok := out.Interface().(Setter); ok {
		setter.SetBSON(d.readDocD())
		return
	}

	switch out.Kind() {
	case reflect.Map:
		d.readMapDocTo(out)
	case reflect.Struct:
		if out.Type() == typeRaw {
			d.readRawDocTo(out)
		} else {
			d.readStructDocTo(out)
		}
	case reflect.Ptr:
		d.readDocTo(out.Elem())
	case reflect.Interface:
		if !out.IsNil() {
			panic("Found non-nil interface. Please contact the developers.")
		}
		mv := reflect.ValueOf(make(M))
		out.Set(mv)
		d.readMapDocTo(mv)
	default:
		panic("TESTME:" + reflect.ValueOf(out).Type().String())
	}
}

func (d *decoder) readMapDocTo(v reflect.Value) {
	vt := v.Type()
	if vt.Key().Kind() != reflect.String {
		panic("BSON map must have string keys. Got: " + v.Type().String())
	}
	elemType := vt.Elem()
	if v.IsNil() {
		v.Set(reflect.MakeMap(v.Type()))
	}
	d.readDocWith(func(kind byte, name string) {
		e := reflect.New(elemType).Elem()
		if d.readElemTo(e, kind) {
			v.SetMapIndex(reflect.ValueOf(name), e)
		}
	})
}

func (d *decoder) readRawDocTo(out reflect.Value) {
	start := d.i
	d.readDocWith(func(kind byte, name string) {
		d.readElemTo(blackHole, kind)
	})
	out.Set(reflect.ValueOf(Raw{0x03, d.in[start:d.i]}))
}

func (d *decoder) readStructDocTo(out reflect.Value) {
	fields, err := getStructFields(out.Type())
	if err != nil {
		panic(err)
	}
	fieldsMap := fields.Map
	d.readDocWith(func(kind byte, name string) {
		if info, ok := fieldsMap[name]; ok {
			d.readElemTo(out.Field(info.Num), kind)
		} else {
			d.dropElem(kind)
		}
	})
}

func (d *decoder) readArrayDoc(t reflect.Type) interface{} {
	tmp := make([]reflect.Value, 0, 8)
	elemType := t.Elem()
	d.readDocWith(func(kind byte, name string) {
		e := reflect.New(elemType).Elem()
		if d.readElemTo(e, kind) {
			tmp = append(tmp, e)
		}
	})
	n := len(tmp)
	slice := reflect.MakeSlice(t, n, n)
	for i := 0; i != n; i++ {
		slice.Index(i).Set(tmp[i])
	}
	return slice.Interface()
}

var typeD = reflect.TypeOf(D{})

func (d *decoder) readDocD() interface{} {
	slice := make(D, 0, 8)
	d.readDocWith(func(kind byte, name string) {
		e := DocElem{Name: name}
		v := reflect.ValueOf(&e.Value)
		if d.readElemTo(v.Elem(), kind) {
			slice = append(slice, e)
		}
	})
	return slice
}

func (d *decoder) readDocWith(f func(kind byte, name string)) {
	end := d.i - 4 + int(d.readInt32())
	if end == d.i || end > len(d.in) || d.in[end-1] != '\x00' {
		corrupted()
	}
	for d.in[d.i] != '\x00' {
		kind, name := d.readElemName()
		if d.i > end {
			corrupted()
		}
		f(kind, name)
		if d.i >= end {
			corrupted()
		}
	}
	d.i++ // '\x00'
	if d.i != end {
		corrupted()
	}
}


// --------------------------------------------------------------------------
// Unmarshaling of individual elements within a document.

var blackHole = settableValueOf(struct{}{})

func (d *decoder) dropElem(kind byte) {
	d.readElemTo(blackHole, kind)
}

// Attempt to decode an element from the document and put it into out.
// If the types are not compatible, the returned ok value will be
// false and out will be unchanged.
func (d *decoder) readElemTo(out reflect.Value, kind byte) (good bool) {

	start := d.i

	if kind == '\x03' {
		// Special case for documents. Delegate to readDocTo().
		switch out.Kind() {
		case reflect.Interface, reflect.Ptr, reflect.Struct, reflect.Map:
			d.readDocTo(out)
		default:
			if _, ok := out.Interface().(D); ok {
				out.Set(reflect.ValueOf(d.readDocD()))
			} else {
				d.readDocTo(blackHole)
			}
		}
		return true
	}

	var in interface{}

	switch kind {
	case '\x01': // Float64
		in = d.readFloat64()
	case '\x02': // UTF-8 string
		in = d.readStr()
	case '\x03': // Document
		panic("Can't happen. Handled above.")
	case '\x04': // Array
		outt := out.Type()
		for outt.Kind() == reflect.Ptr {
			outt = outt.Elem()
		}
		switch outt.Kind() {
		case reflect.Slice:
			in = d.readArrayDoc(outt)
		default:
			sliceType := reflect.TypeOf([]interface{}{}) // XXX Cache this.
			in = d.readArrayDoc(sliceType)
		}
	case '\x05': // Binary
		b := d.readBinary()
		if b.Kind == 0x00 {
			in = b.Data
		} else {
			in = b
		}
	case '\x06': // Undefined (obsolete, but still seen in the wild)
		in = Undefined
	case '\x07': // ObjectId
		in = ObjectId(d.readBytes(12))
	case '\x08': // Bool
		in = d.readBool()
	case '\x09': // Timestamp
		// MongoDB wants timestamps as milliseconds.
		// Go likes nanoseconds.  Convert them.
		in = Timestamp(d.readInt64() * 1e6)
	case '\x0A': // Nil
		in = nil
	case '\x0B': // RegEx
		in = d.readRegEx()
	case '\x0D': // JavaScript without scope
		in = JS{Code: d.readStr()}
	case '\x0E': // Symbol
		in = Symbol(d.readStr())
	case '\x0F': // JavaScript with scope
		d.i += 4 // Skip length
		js := JS{d.readStr(), make(M)}
		d.readDocTo(reflect.ValueOf(js.Scope))
		in = js
	case '\x10': // Int32
		in = int(d.readInt32())
	case '\x11': // Mongo-specific timestamp
		in = MongoTimestamp(d.readInt64())
	case '\x12': // Int64
		in = d.readInt64()
	case '\x7F': // Max key
		in = MaxKey
	case '\xFF': // Min key
		in = MinKey
	default:
		panic(fmt.Sprintf("Unknown element kind (0x%02X)", kind))
	}

	if out.Type() == typeRaw {
		out.Set(reflect.ValueOf(Raw{kind, d.in[start:d.i]}))
		return true
	}

	if setter, ok := out.Interface().(Setter); ok {
		if zeroNilPtr(out) {
			setter = out.Interface().(Setter)
		}
		return setter.SetBSON(in)
	}

	if in == nil {
		out.Set(reflect.Zero(out.Type()))
		return true
	}

	// Dereference and initialize pointer if necessary.
	first := true
	for out.Kind() == reflect.Ptr {
		if !out.IsNil() {
			out = out.Elem()
			continue
		}

		elem := reflect.New(out.Type().Elem())
		if first {
			// Only set if value is compatible.
			first = false
			defer func(out, elem reflect.Value) {
				if good {
					out.Set(elem)
				}
			}(out, elem)
		} else {
			out.Set(elem)
		}
		out = elem
	}

	inv := reflect.ValueOf(in)
	if out.Type() == inv.Type() {
		out.Set(inv)
		return true
	}

	switch out.Kind() {
	case reflect.Interface:
		out.Set(inv)
		return true
	case reflect.String:
		switch inv.Kind() {
		case reflect.String:
			out.SetString(inv.String())
			return true
		case reflect.Slice:
			if b, ok := in.([]byte); ok {
				out.SetString(string(b))
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		// Remember, array (0x04) slices are built with the correct element
		// type.  If we are here, must be a cross BSON kind conversion.
		if out.Type().Elem().Kind() == reflect.Uint8 {
			switch inv.Kind() {
			case reflect.String:
				slice := []byte(inv.String())
				out.Set(reflect.ValueOf(slice))
				return true
			case reflect.Slice:
				// out must be an array. A slice would trigger
				// inv.Type() == out.Type() above.
				reflect.Copy(out, inv)
				return true
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch inv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			// MongoDB wants timestamps as milliseconds.
			// Go likes nanoseconds.  Convert them.
			// out.Type() == inv.Type() has been handled above.
			if out.Type() == typeTimestamp {
				out.SetInt(inv.Int() * 1e6)
			} else if inv.Type() == typeTimestamp {
				out.SetInt(inv.Int() / 1e6)
			} else {
				out.SetInt(inv.Int())
			}
			return true
		case reflect.Float32, reflect.Float64:
			out.SetInt(int64(inv.Float()))
			return true
		case reflect.Bool:
			if inv.Bool() {
				out.SetInt(1)
			} else {
				out.SetInt(0)
			}
			return true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			panic("Can't happen. No uint types in BSON?")
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		switch inv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			out.SetUint(uint64(inv.Int()))
			return true
		case reflect.Float32, reflect.Float64:
			out.SetUint(uint64(inv.Float()))
			return true
		case reflect.Bool:
			if inv.Bool() {
				out.SetUint(1)
			} else {
				out.SetUint(0)
			}
			return true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			panic("Can't happen. No uint types in BSON?")
		}
	case reflect.Float32, reflect.Float64:
		switch inv.Kind() {
		case reflect.Float32, reflect.Float64:
			out.SetFloat(inv.Float())
			return true
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			out.SetFloat(float64(inv.Int()))
			return true
		case reflect.Bool:
			if inv.Bool() {
				out.SetFloat(1)
			} else {
				out.SetFloat(0)
			}
			return true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			panic("Can't happen. No uint types in BSON?")
		}
	case reflect.Bool:
		switch inv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			out.SetBool(inv.Int() != 0)
			return true
		case reflect.Float32, reflect.Float64:
			out.SetBool(inv.Float() != 0)
			return true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			panic("Can't happen. No uint types in BSON?")
		}
	}

	return false
}


// --------------------------------------------------------------------------
// Parsers of basic types.

func (d *decoder) readElemName() (kind byte, name string) {
	return d.readByte(), d.readCStr()
}

func (d *decoder) readRegEx() RegEx {
	re := RegEx{}
	re.Pattern = d.readCStr()
	re.Options = d.readCStr()
	return re
}

func (d *decoder) readBinary() Binary {
	l := d.readInt32()
	b := Binary{}
	b.Kind = d.readByte()
	b.Data = d.readBytes(l)
	if b.Kind == 0x02 {
		// Weird obsolete format with redundant length.
		b.Data = b.Data[4:]
	}
	return b
}

func (d *decoder) readStr() string {
	l := d.readInt32()
	b := d.readBytes(l - 1)
	if d.readByte() != '\x00' {
		corrupted()
	}
	return string(b)
}

func (d *decoder) readCStr() string {
	return string(d.readBytesUpto('\x00'))
}

func (d *decoder) readBool() bool {
	if d.readByte() == 1 {
		return true
	}
	return false
}

func (d *decoder) readFloat64() float64 {
	return math.Float64frombits(uint64(d.readInt64()))
}

func (d *decoder) readInt32() int32 {
	b := d.readBytes(4)
	return int32((uint32(b[0]) << 0) |
		(uint32(b[1]) << 8) |
		(uint32(b[2]) << 16) |
		(uint32(b[3]) << 24))
}

func (d *decoder) readInt64() int64 {
	b := d.readBytes(8)
	return int64((uint64(b[0]) << 0) |
		(uint64(b[1]) << 8) |
		(uint64(b[2]) << 16) |
		(uint64(b[3]) << 24) |
		(uint64(b[4]) << 32) |
		(uint64(b[5]) << 40) |
		(uint64(b[6]) << 48) |
		(uint64(b[7]) << 56))
}

func (d *decoder) readByte() byte {
	i := d.i
	d.i++
	if d.i > len(d.in) {
		corrupted()
	}
	return d.in[i]
}

func (d *decoder) readBytes(length int32) []byte {
	start := d.i
	d.i += int(length)
	if d.i > len(d.in) {
		corrupted()
	}
	return d.in[start : start+int(length)]
}

func (d *decoder) readBytesUpto(delimiter byte) []byte {
	start := d.i
	end := start
	l := len(d.in)
	for ; end != l; end++ {
		if d.in[end] == delimiter {
			break
		}
	}
	d.i = end + 1
	if d.i > l {
		corrupted()
	}
	return d.in[start:end]
}
