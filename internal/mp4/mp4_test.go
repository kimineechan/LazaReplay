package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// A baseline-profile parameter set for a 320 by 240 picture, and a picture
// parameter set to go with it.
var (
	testSPS = []byte{0x67, 0x42, 0x00, 0x1E, 0xF4, 0x0A, 0x0F, 0xC8}
	testPPS = []byte{0x68, 0xCE, 0x3C, 0x80}
)

// boxes splits data into the boxes it holds.
func boxes(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	found := map[string][]byte{}
	for len(data) > 0 {
		if len(data) < 8 {
			t.Fatalf("%d stray bytes where a box should be", len(data))
		}
		size, head := uint64(binary.BigEndian.Uint32(data)), 8
		if size == 1 {
			size, head = binary.BigEndian.Uint64(data[8:]), 16
		}
		if size < uint64(head) || size > uint64(len(data)) {
			t.Fatalf("box %q claims %d bytes of %d", data[4:8], size, len(data))
		}
		kind := string(data[4:8])
		if _, twice := found[kind]; twice {
			kind += "#2"
		}
		found[kind] = data[head:size]
		data = data[size:]
	}
	return found
}

func be32(b []byte, at int) int { return int(binary.BigEndian.Uint32(b[at:])) }

// samples returns the bytes of every sample of a track, located through the
// track's own tables as a player would locate them.
func samples(t *testing.T, file, stbl []byte) [][]byte {
	t.Helper()
	tables := boxes(t, stbl)
	var at []int
	if co, ok := tables["stco"]; ok {
		for i := 0; i < be32(co, 4); i++ {
			at = append(at, be32(co, 8+4*i))
		}
	} else {
		co := tables["co64"]
		for i := 0; i < be32(co, 4); i++ {
			at = append(at, int(binary.BigEndian.Uint64(co[8+8*i:])))
		}
	}
	stsc := tables["stsc"]
	perChunk := make([]int, len(at))
	for i := 0; i < be32(stsc, 4); i++ {
		first, count := be32(stsc, 8+12*i), be32(stsc, 12+12*i)
		for c := first - 1; c < len(at); c++ {
			perChunk[c] = count
		}
	}
	stsz := tables["stsz"]
	var out [][]byte
	n := 0
	for c, offset := range at {
		for i := 0; i < perChunk[c]; i++ {
			size := be32(stsz, 12+4*n)
			out = append(out, file[offset:offset+size])
			offset += size
			n++
		}
	}
	if n != be32(stsz, 8) {
		t.Fatalf("chunks hold %d samples, size table lists %d", n, be32(stsz, 8))
	}
	return out
}

func build(large bool) (*Movie, [][]byte, [][]byte) {
	m := &Movie{Created: time.Date(2026, 11, 12, 14, 3, 22, 0, time.UTC), large: large}
	m.Video.SPS, m.Video.PPS = testSPS, testPPS
	m.Audio = &Audio{Rate: 48000, Channels: 2, Bitrate: 160000, Delay: 480}
	var pictures, blocks [][]byte
	for i := 0; i < 25; i++ {
		pictures = append(pictures, bytes.Repeat([]byte{byte(i)}, i+3))
		m.Video.Sizes = append(m.Video.Sizes, uint32(i+3))
		duration := uint32(1500)
		if i%7 == 6 {
			duration = 3000
		}
		m.Video.Durations = append(m.Video.Durations, duration)
		m.Video.Keys = append(m.Video.Keys, i%10 == 0)
	}
	for i := 0; i < 40; i++ {
		blocks = append(blocks, bytes.Repeat([]byte{byte(0x80 + i)}, 5+i%3))
		m.Audio.Sizes = append(m.Audio.Sizes, uint32(5+i%3))
	}
	join := func(list [][]byte) []byte { return bytes.Join(list, nil) }
	m.Chunks = []Chunk{
		{Samples: 10, Data: join(pictures[0:10])}, {Audio: true, Samples: 15, Data: join(blocks[0:15])},
		{Samples: 10, Data: join(pictures[10:20])}, {Audio: true, Samples: 15, Data: join(blocks[15:30])},
		{Samples: 5, Data: join(pictures[20:25])}, {Audio: true, Samples: 10, Data: join(blocks[30:40])},
	}
	return m, pictures, blocks
}

func TestFileRoundTrip(t *testing.T) {
	for _, large := range []bool{false, true} {
		m, pictures, blocks := build(large)
		var out bytes.Buffer
		n, err := m.WriteTo(&out)
		if err != nil {
			t.Fatal(err)
		}
		file := out.Bytes()
		if size, _ := m.Size(); n != int64(len(file)) || size != n {
			t.Fatalf("wrote %d bytes, reported %d, Size says %d", len(file), n, size)
		}
		top := boxes(t, file)
		if string(top["ftyp"][:4]) != "isom" {
			t.Fatalf("brand %q", top["ftyp"][:4])
		}
		if !bytes.HasPrefix(file[len(file)-len(top["mdat"]):], top["mdat"]) || len(top["mdat"]) == 0 {
			t.Fatal("picture data is not the last box")
		}
		moov := boxes(t, top["moov"])
		mvhd := moov["mvhd"]
		if be32(mvhd, 12) != VideoScale {
			t.Fatalf("movie time scale %d", be32(mvhd, 12))
		}
		wantLength := 22*1500 + 3*3000 // 25 pictures, three shown twice as long
		audioLength := 40*1024*VideoScale/48000 + 480*VideoScale/48000
		if got := be32(mvhd, 16); got != max(wantLength, audioLength) {
			t.Fatalf("movie length %d, want %d", got, max(wantLength, audioLength))
		}

		video := boxes(t, moov["trak"])
		tkhd := video["tkhd"]
		if w, h := be32(tkhd, 76)>>16, be32(tkhd, 80)>>16; w != 320 || h != 240 {
			t.Fatalf("picture size %dx%d", w, h)
		}
		stbl := boxes(t, boxes(t, video["mdia"])["minf"])["stbl"]
		for i, s := range samples(t, file, stbl) {
			if !bytes.Equal(s, pictures[i]) {
				t.Fatalf("large=%v: picture %d reads back as %v", large, i, s)
			}
		}
		tables := boxes(t, stbl)
		stss := tables["stss"]
		if be32(stss, 4) != 3 || be32(stss, 8) != 1 || be32(stss, 12) != 11 || be32(stss, 16) != 21 {
			t.Fatalf("keyframe table %v", stss)
		}
		stts, total, count := tables["stts"], 0, 0
		for i := 0; i < be32(stts, 4); i++ {
			count += be32(stts, 8+8*i)
			total += be32(stts, 8+8*i) * be32(stts, 12+8*i)
		}
		if count != 25 || total != wantLength {
			t.Fatalf("timing table covers %d pictures over %d units", count, total)
		}
		entry := tables["stsd"][8:]
		if string(entry[4:8]) != "avc1" || be32(entry, 32)>>16 != 320 {
			t.Fatalf("sample entry %q", entry[4:8])
		}
		inner := boxes(t, entry[86:be32(entry, 0)])
		avcC := inner["avcC"]
		if avcC[0] != 1 || avcC[1] != 0x42 || avcC[3] != 0x1E || avcC[4]&3 != 3 {
			t.Fatalf("decoder record %v", avcC)
		}
		if !bytes.Equal(avcC[8:8+len(testSPS)], testSPS) || !bytes.HasSuffix(avcC, testPPS) {
			t.Fatal("parameter sets are not in the decoder record")
		}
		if colr := inner["colr"]; string(colr[:4]) != "nclx" || colr[5] != 1 || colr[9] != 1 {
			t.Fatalf("colour box %v", colr)
		}

		audio := boxes(t, moov["trak#2"])
		elst := boxes(t, audio["edts"])["elst"]
		if be32(elst, 4) != 2 || be32(elst, 8) != 480*VideoScale/48000 || uint32(be32(elst, 12)) != 0xFFFFFFFF {
			t.Fatalf("edit list %v", elst)
		}
		stbl = boxes(t, boxes(t, audio["mdia"])["minf"])["stbl"]
		for i, s := range samples(t, file, stbl) {
			if !bytes.Equal(s, blocks[i]) {
				t.Fatalf("large=%v: sound block %d reads back as %v", large, i, s)
			}
		}
		entry = boxes(t, stbl)["stsd"][8:]
		if string(entry[4:8]) != "mp4a" || be32(entry, 32)>>16 != 48000 {
			t.Fatalf("sound entry %q at rate %d", entry[4:8], be32(entry, 32)>>16)
		}
		esds := boxes(t, entry[36:be32(entry, 0)])["esds"]
		if !bytes.Contains(esds, []byte{0x05, 0x02, 0x11, 0x90}) {
			t.Fatalf("AAC description %v", esds)
		}
	}
}

func TestSilentClip(t *testing.T) {
	m, _, _ := build(false)
	m.Audio = nil
	m.Chunks = []Chunk{m.Chunks[0], m.Chunks[2], m.Chunks[4]}
	var out bytes.Buffer
	if _, err := m.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	moov := boxes(t, boxes(t, out.Bytes())["moov"])
	if _, ok := moov["trak#2"]; ok {
		t.Fatal("silent clip has a second track")
	}
	if be32(moov["mvhd"], 96) != 2 {
		t.Fatalf("next track number %d", be32(moov["mvhd"], 96))
	}
}

func TestRefusesInconsistentInput(t *testing.T) {
	cases := map[string]func(*Movie){
		"missing picture":   func(m *Movie) { m.Chunks[0].Samples = 9 },
		"missing bytes":     func(m *Movie) { m.Chunks[2].Data = m.Chunks[2].Data[1:] },
		"sound mismatch":    func(m *Movie) { m.Audio.Sizes = m.Audio.Sizes[1:] },
		"no keyframe first": func(m *Movie) { m.Video.Keys[0] = false },
		"short table":       func(m *Movie) { m.Video.Durations = m.Video.Durations[1:] },
		"no parameter set":  func(m *Movie) { m.Video.PPS = nil },
		"bad parameter set": func(m *Movie) { m.Video.SPS = []byte{0x67, 1, 2} },
		"nothing":           func(m *Movie) { m.Video.Sizes, m.Video.Durations, m.Video.Keys = nil, nil, nil },
	}
	for name, spoil := range cases {
		m, _, _ := build(false)
		spoil(m)
		var out bytes.Buffer
		if _, err := m.WriteTo(&out); err == nil {
			t.Errorf("%s: wrote a file from inconsistent input", name)
		}
		if out.Len() != 0 {
			t.Errorf("%s: wrote %d bytes before refusing", name, out.Len())
		}
	}
}

func TestConfig(t *testing.T) {
	if c := Config(48000, 2); !bytes.Equal(c, []byte{0x11, 0x90}) {
		t.Fatalf("48 kHz stereo: %X", c)
	}
	if c := Config(44100, 2); !bytes.Equal(c, []byte{0x12, 0x10}) {
		t.Fatalf("44.1 kHz stereo: %X", c)
	}
}
