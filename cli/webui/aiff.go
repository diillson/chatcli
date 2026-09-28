/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"encoding/binary"
)

// aiffToWAV rewraps 16-bit PCM AIFF/AIFF-C (what macOS `say` writes, the
// system voice fallback) as WAV, which every browser plays; Chrome and
// Firefox cannot play AIFF. Anything else reports ok=false and is served as
// it came.
func aiffToWAV(data []byte) ([]byte, bool) {
	if len(data) < 12 || string(data[0:4]) != "FORM" {
		return nil, false
	}
	form := string(data[8:12])
	if form != "AIFF" && form != "AIFC" {
		return nil, false
	}
	var channels, bits uint16
	var rate uint32
	littleEndian := false
	var pcm []byte
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if size < 0 || body+size > len(data) {
			size = len(data) - body // truncated trailing chunk: take what is there
		}
		chunk := data[body : body+size]
		switch id {
		case "COMM":
			if len(chunk) < 18 {
				return nil, false
			}
			channels = binary.BigEndian.Uint16(chunk[0:2])
			bits = binary.BigEndian.Uint16(chunk[6:8])
			rate = extendedToUint32(chunk[8:18])
			if form == "AIFC" && len(chunk) >= 22 {
				switch string(chunk[18:22]) {
				case "NONE", "twos":
				case "sowt":
					littleEndian = true
				default:
					return nil, false // compressed: not ours to decode
				}
			}
		case "SSND":
			if len(chunk) < 8 {
				return nil, false
			}
			start := 8 + int(binary.BigEndian.Uint32(chunk[0:4]))
			if start > len(chunk) {
				return nil, false
			}
			pcm = chunk[start:]
		}
		off = body + size + size%2 // chunks are padded to even sizes
	}
	if bits != 16 || channels == 0 || rate == 0 || len(pcm) < 2 {
		return nil, false
	}
	pcm = pcm[:len(pcm)&^1]
	out := make([]byte, len(pcm))
	if littleEndian {
		copy(out, pcm)
	} else {
		for i := 0; i+1 < len(pcm); i += 2 {
			out[i], out[i+1] = pcm[i+1], pcm[i]
		}
	}
	return wavHeader(out, channels, rate), true
}

// extendedToUint32 converts the 80-bit IEEE extended sample rate AIFF uses.
func extendedToUint32(b []byte) uint32 {
	exp := int(binary.BigEndian.Uint16(b[0:2])&0x7FFF) - 16383
	mant := binary.BigEndian.Uint64(b[2:10])
	if exp < 0 || exp > 31 {
		return 0
	}
	return uint32(mant >> uint(63-exp)) // #nosec G115 -- exp bounds the value to 32 bits
}

// wavHeader wraps little-endian 16-bit PCM in a canonical RIFF/WAVE header.
func wavHeader(pcm []byte, channels uint16, rate uint32) []byte {
	var buf bytes.Buffer
	blockAlign := channels * 2
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm))) // #nosec G115 -- bounded by the upload cap
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, channels)
	_ = binary.Write(&buf, binary.LittleEndian, rate)
	_ = binary.Write(&buf, binary.LittleEndian, rate*uint32(blockAlign))
	_ = binary.Write(&buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pcm))) // #nosec G115 -- bounded by the upload cap
	buf.Write(pcm)
	return buf.Bytes()
}
