package runtype

import (
	"encoding/binary"
	"sync"
	"unsafe"
)

// names interns encoded name records by content. A record is immutable once
// built, so every type with the same name shares one copy, as the linker
// shares name data in a binary: a reload of the same code then adds no name
// bytes and no reflect-offset entries for them (addReflectOff dedups by
// pointer). Both would otherwise be kept for good. The table holds each
// distinct record once, for the life of the process.
var names sync.Map // string (record bytes) -> *byte (into that string's data)

func internName(buf []byte) abiName {
	if p, ok := names.Load(string(buf)); ok {
		return abiName{Bytes: p.(*byte)}
	}
	s := string(buf)
	p, _ := names.LoadOrStore(s, unsafe.StringData(s))
	return abiName{Bytes: p.(*byte)}
}

// encodeName produces an internal/abi.Name-format byte record.
// Format: one flag byte (bit0=exported, bit1=has-tag, bit2=has-pkgpath,
// bit3=embedded), then uvarint(len(name)) and the name bytes.
// We avoid linknaming reflect.newName to keep the package buildable without
// -checklinkname=0.
// Names have no tag; pkgpath is handled separately by encodeNamePkg.
func encodeName(name string, exported bool) abiName {
	var flags byte
	if exported {
		flags |= 0b0001
	}
	var lenBuf [binary.MaxVarintLen64]byte
	lenSize := binary.PutUvarint(lenBuf[:], uint64(len(name)))

	buf := make([]byte, 1+lenSize+len(name))
	buf[0] = flags
	copy(buf[1:], lenBuf[:lenSize])
	copy(buf[1+lenSize:], name)
	return internName(buf)
}

// Sets the embedded bit reflect.StructOf refuses on an unexported embed (anon+PkgPath).
// A field's PkgPath lives on the struct type, so none is encoded here.
func encodeFieldName(name, tag string, exported, embedded bool) abiName {
	var flags byte
	if exported {
		flags |= 0b0001
	}
	if tag != "" {
		flags |= 0b0010
	}
	if embedded {
		flags |= 0b1000
	}
	var nLen, tLen [binary.MaxVarintLen64]byte
	nSize := binary.PutUvarint(nLen[:], uint64(len(name)))
	total := 1 + nSize + len(name)
	tSize := 0
	if tag != "" {
		tSize = binary.PutUvarint(tLen[:], uint64(len(tag)))
		total += tSize + len(tag)
	}
	buf := make([]byte, total)
	buf[0] = flags
	p := 1 + copy(buf[1:], nLen[:nSize])
	p += copy(buf[p:], name)
	if tag != "" {
		p += copy(buf[p:], tLen[:tSize])
		copy(buf[p:], tag)
	}
	return internName(buf)
}

// encodeNamePkg is encodeName with an embedded import path (abi.Name bit2), so
// native reflect.Implements can match an unexported method against an unexported
// interface method. Layout: flags(bit2) | uvarint(len) | name | 4-byte nameOff
// to the path's name record (native order). pkgPath == "" uses the plain form.
func encodeNamePkg(name string, exported bool, pkgPath string) abiName {
	if pkgPath == "" {
		return encodeName(name, exported)
	}
	pkgOff := addReflectOff(unsafe.Pointer(encodeName(pkgPath, false).Bytes))

	var flags byte = 0b0100 // bit2: import path follows
	if exported {
		flags |= 0b0001
	}
	var lenBuf [binary.MaxVarintLen64]byte
	lenSize := binary.PutUvarint(lenBuf[:], uint64(len(name)))

	buf := make([]byte, 1+lenSize+len(name)+4)
	buf[0] = flags
	copy(buf[1:], lenBuf[:lenSize])
	copy(buf[1+lenSize:], name)
	binary.NativeEndian.PutUint32(buf[1+lenSize+len(name):], uint32(pkgOff))
	return internName(buf)
}
