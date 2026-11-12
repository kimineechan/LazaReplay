// Package mp4 writes an MP4 file from compressed video and sound that are
// already in memory.
//
// The file is laid out for instant playback: the index of every picture
// comes first and the pictures themselves after it, so a player can start,
// or jump to any point, without reading to the end of the file. That layout
// needs every size to be known before the first byte is written, which is
// the case here because a clip is complete before it is saved.
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"lazareplay/internal/h264"
)

// VideoScale is the number of time units per second on the video track.
const VideoScale = 90000

// Video describes the picture track.
type Video struct {
	SPS, PPS  []byte   // parameter sets, each with its one-byte header
	Sizes     []uint32 // bytes in each picture
	Durations []uint32 // how long each picture is shown, in VideoScale units
	Keys      []bool   // whether each picture is a keyframe
}

// Audio describes the sound track: AAC, 1024 samples to a block.
type Audio struct {
	Rate, Channels int
	Bitrate        int      // bits per second, for the file's description
	Sizes          []uint32 // bytes in each block
	Delay          int64    // how long after the video the sound starts, in 1/Rate s
}

// Chunk is a run of consecutive pictures, or of consecutive sound blocks,
// stored together. Chunks are written in the order given; alternating video
// and sound chunks keeps the two close together in the file.
type Chunk struct {
	Audio   bool
	Samples int
	Data    []byte
}

// Movie is everything that goes into one file.
type Movie struct {
	Video   Video
	Audio   *Audio // nil for a silent clip
	Chunks  []Chunk
	Created time.Time

	large bool // use 64-bit offsets; set automatically above 4 GB
}

const audioBlock = 1024

func u16(v int) []byte    { return binary.BigEndian.AppendUint16(nil, uint16(v)) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
func zeros(n int) []byte  { return make([]byte, n) }

// box builds one box: its length, its four-letter kind, then its contents.
func box(kind string, parts ...[]byte) []byte {
	size := 8
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	out = binary.BigEndian.AppendUint32(out, uint32(size))
	out = append(out, kind...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// full builds a box that starts with a version byte and three flag bytes.
func full(kind string, version byte, flags uint32, parts ...[]byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(kind, append([][]byte{head}, parts...)...)
}

var matrix = [9]uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func identity() []byte {
	var out []byte
	for _, v := range matrix {
		out = binary.BigEndian.AppendUint32(out, v)
	}
	return out
}

// check verifies that the tables and the chunks describe the same data.
func (m *Movie) check() error {
	v := &m.Video
	if len(v.Sizes) == 0 {
		return errors.New("mp4: no pictures to write")
	}
	if len(v.Durations) != len(v.Sizes) || len(v.Keys) != len(v.Sizes) {
		return errors.New("mp4: picture tables differ in length")
	}
	if !v.Keys[0] {
		return errors.New("mp4: the first picture is not a keyframe")
	}
	var pictures, blocks int
	var pictureBytes, blockBytes, videoBytes, audioBytes uint64
	for _, c := range m.Chunks {
		if c.Samples <= 0 {
			return errors.New("mp4: empty chunk")
		}
		if c.Audio {
			blocks += c.Samples
			audioBytes += uint64(len(c.Data))
		} else {
			pictures += c.Samples
			videoBytes += uint64(len(c.Data))
		}
	}
	for _, s := range v.Sizes {
		pictureBytes += uint64(s)
	}
	if pictures != len(v.Sizes) || pictureBytes != videoBytes {
		return fmt.Errorf("mp4: chunks hold %d pictures in %d bytes, tables say %d in %d", pictures, videoBytes, len(v.Sizes), pictureBytes)
	}
	if m.Audio == nil {
		if blocks != 0 {
			return errors.New("mp4: sound chunks without a sound track")
		}
		return nil
	}
	for _, s := range m.Audio.Sizes {
		blockBytes += uint64(s)
	}
	if blocks != len(m.Audio.Sizes) || blockBytes != audioBytes {
		return fmt.Errorf("mp4: chunks hold %d sound blocks in %d bytes, tables say %d in %d", blocks, audioBytes, len(m.Audio.Sizes), blockBytes)
	}
	return nil
}

// header builds everything that precedes the pictures.
func (m *Movie) header() ([]byte, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	sps, err := h264.ParseSPS(m.Video.SPS)
	if err != nil {
		return nil, err
	}
	if len(m.Video.PPS) == 0 {
		return nil, errors.New("mp4: no picture parameter set")
	}
	var data uint64
	for _, c := range m.Chunks {
		data += uint64(len(c.Data))
	}
	ftyp := box("ftyp", []byte("isom"), u32(0x200), []byte("isomiso2avc1mp41"))
	large := m.large
	for {
		lead := 8
		if large {
			lead = 16
		}
		// The index is built twice: once to learn its length, which fixes
		// where the pictures will lie, then again with those positions.
		length := len(m.moov(sps, 0, large))
		first := uint64(len(ftyp) + length + lead)
		if !large && first+data > 0xFFFFFFFF {
			large = true
			continue
		}
		out := append(ftyp, m.moov(sps, first, large)...)
		if large {
			out = append(out, u32(1)...)
			out = append(out, "mdat"...)
			out = append(out, u64(data+16)...)
		} else {
			out = append(out, u32(uint32(data+8))...)
			out = append(out, "mdat"...)
		}
		return out, nil
	}
}

// Size returns the length of the whole file in bytes.
func (m *Movie) Size() (int64, error) {
	head, err := m.header()
	if err != nil {
		return 0, err
	}
	size := int64(len(head))
	for _, c := range m.Chunks {
		size += int64(len(c.Data))
	}
	return size, nil
}

// WriteTo writes the file.
func (m *Movie) WriteTo(w io.Writer) (int64, error) {
	head, err := m.header()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(head)
	written := int64(n)
	if err != nil {
		return written, err
	}
	for _, c := range m.Chunks {
		n, err = w.Write(c.Data)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (m *Movie) moov(sps h264.SPS, first uint64, large bool) []byte {
	// Positions of the chunks of each track.
	var videoAt, audioAt []uint64
	var videoRuns, audioRuns []int
	at := first
	for _, c := range m.Chunks {
		if c.Audio {
			audioAt, audioRuns = append(audioAt, at), append(audioRuns, c.Samples)
		} else {
			videoAt, videoRuns = append(videoAt, at), append(videoRuns, c.Samples)
		}
		at += uint64(len(c.Data))
	}
	created := uint32(0)
	if !m.Created.IsZero() {
		created = uint32(m.Created.Unix() + 2082844800) // seconds since 1904
	}
	var videoLength uint64
	for _, d := range m.Video.Durations {
		videoLength += uint64(d)
	}
	length := videoLength // of the whole movie, in VideoScale units
	tracks := [][]byte{m.videoTrack(sps, created, videoLength, videoAt, videoRuns, large)}
	next := uint32(2)
	if a := m.Audio; a != nil && len(a.Sizes) > 0 {
		media := uint64(len(a.Sizes)) * audioBlock
		delay := uint64(a.Delay) * VideoScale / uint64(a.Rate)
		shown := media*VideoScale/uint64(a.Rate) + delay
		length = max(length, shown)
		tracks = append(tracks, m.audioTrack(created, media, delay, shown, audioAt, audioRuns, large))
		next = 3
	}
	mvhd := full("mvhd", 0, 0,
		u32(created), u32(created), u32(VideoScale), u32(uint32(length)),
		u32(0x00010000), u16(0x0100), zeros(10), identity(), zeros(24), u32(next))
	return box("moov", append([][]byte{mvhd}, tracks...)...)
}

func (m *Movie) videoTrack(sps h264.SPS, created uint32, length uint64, at []uint64, runs []int, large bool) []byte {
	v := &m.Video
	tkhd := full("tkhd", 0, 3,
		u32(created), u32(created), u32(1), zeros(4), u32(uint32(length)),
		zeros(8), u16(0), u16(0), u16(0), u16(0), identity(),
		u32(uint32(sps.Width)<<16), u32(uint32(sps.Height)<<16))
	mdhd := full("mdhd", 0, 0, u32(created), u32(created), u32(VideoScale), u32(uint32(length)), u16(0x55C4), u16(0))
	hdlr := full("hdlr", 0, 0, zeros(4), []byte("vide"), zeros(12), []byte("VideoHandler\x00"))

	avcC := []byte{1, v.SPS[1], v.SPS[2], v.SPS[3], 0xFF, 0xE1}
	avcC = append(avcC, u16(len(v.SPS))...)
	avcC = append(avcC, v.SPS...)
	avcC = append(avcC, 1)
	avcC = append(avcC, u16(len(v.PPS))...)
	avcC = append(avcC, v.PPS...)
	switch sps.Profile {
	case 100, 110, 122, 144:
		avcC = append(avcC, 0xFC|byte(sps.ChromaFormat), 0xF8|byte(sps.BitDepthLuma-8), 0xF8|byte(sps.BitDepthChroma-8), 0)
	}
	// The pictures are BT.709 at studio range; say so for players that ask.
	colr := box("colr", []byte("nclx"), u16(1), u16(1), u16(1), []byte{0})
	avc1 := box("avc1",
		zeros(6), u16(1), zeros(16),
		u16(sps.Width), u16(sps.Height), u32(0x00480000), u32(0x00480000),
		zeros(4), u16(1), zeros(32), u16(0x0018), u16(0xFFFF),
		box("avcC", avcC), colr)
	stsd := full("stsd", 0, 0, u32(1), avc1)

	var stts []byte
	entries := uint32(0)
	for i := 0; i < len(v.Durations); {
		j := i
		for j < len(v.Durations) && v.Durations[j] == v.Durations[i] {
			j++
		}
		stts = binary.BigEndian.AppendUint32(stts, uint32(j-i))
		stts = binary.BigEndian.AppendUint32(stts, v.Durations[i])
		entries++
		i = j
	}
	var stss []byte
	keys := uint32(0)
	for i, key := range v.Keys {
		if key {
			stss = binary.BigEndian.AppendUint32(stss, uint32(i+1))
			keys++
		}
	}
	stbl := box("stbl", stsd,
		full("stts", 0, 0, u32(entries), stts),
		full("stss", 0, 0, u32(keys), stss),
		chunkMap(runs), sizes(v.Sizes), offsets(at, large))
	minf := box("minf", full("vmhd", 0, 1, zeros(8)), dataReference(), stbl)
	return box("trak", tkhd, box("mdia", mdhd, hdlr, minf))
}

func (m *Movie) audioTrack(created uint32, media, delay, shown uint64, at []uint64, runs []int, large bool) []byte {
	a := m.Audio
	tkhd := full("tkhd", 0, 3,
		u32(created), u32(created), u32(2), zeros(4), u32(uint32(shown)),
		zeros(8), u16(0), u16(1), u16(0x0100), u16(0), identity(), u32(0), u32(0))
	parts := [][]byte{tkhd}
	if delay > 0 {
		// An edit list: stay silent for the delay, then play the track.
		parts = append(parts, box("edts", full("elst", 0, 0, u32(2),
			u32(uint32(delay)), u32(0xFFFFFFFF), u32(0x00010000),
			u32(uint32(shown-delay)), u32(0), u32(0x00010000))))
	}
	mdhd := full("mdhd", 0, 0, u32(created), u32(created), u32(uint32(a.Rate)), u32(uint32(media)), u16(0x55C4), u16(0))
	hdlr := full("hdlr", 0, 0, zeros(4), []byte("soun"), zeros(12), []byte("SoundHandler\x00"))

	config := Config(a.Rate, a.Channels)
	decoder := append([]byte{0x40, 0x15, 0, 0, 0}, u32(uint32(a.Bitrate))...)
	decoder = append(decoder, u32(uint32(a.Bitrate))...)
	decoder = append(decoder, 0x05, byte(len(config)))
	decoder = append(decoder, config...)
	es := append([]byte{0, 0, 0, 0x04, byte(len(decoder))}, decoder...)
	es = append(es, 0x06, 1, 0x02)
	esds := full("esds", 0, 0, []byte{0x03, byte(len(es))}, es)
	mp4a := box("mp4a",
		zeros(6), u16(1), zeros(8),
		u16(a.Channels), u16(16), zeros(4), u32(uint32(a.Rate)<<16), esds)
	stsd := full("stsd", 0, 0, u32(1), mp4a)
	stbl := box("stbl", stsd,
		full("stts", 0, 0, u32(1), u32(uint32(len(a.Sizes))), u32(audioBlock)),
		chunkMap(runs), sizes(a.Sizes), offsets(at, large))
	minf := box("minf", full("smhd", 0, 0, zeros(4)), dataReference(), stbl)
	parts = append(parts, box("mdia", mdhd, hdlr, minf))
	return box("trak", parts...)
}

// Config returns the two bytes that describe AAC-LC sound to a decoder.
func Config(rate, channels int) []byte {
	index := 3 // 48000
	for i, r := range []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000} {
		if r == rate {
			index = i
		}
	}
	v := 2<<11 | index<<7 | channels<<3
	return []byte{byte(v >> 8), byte(v)}
}

// dataReference says the data is in this same file.
func dataReference() []byte {
	return box("dinf", full("dref", 0, 0, u32(1), full("url ", 0, 1)))
}

// chunkMap is the table that says how many samples each chunk holds. Runs
// of chunks with the same count share one entry.
func chunkMap(runs []int) []byte {
	var table []byte
	entries := uint32(0)
	for i, n := range runs {
		if i > 0 && runs[i-1] == n {
			continue
		}
		table = binary.BigEndian.AppendUint32(table, uint32(i+1))
		table = binary.BigEndian.AppendUint32(table, uint32(n))
		table = binary.BigEndian.AppendUint32(table, 1)
		entries++
	}
	return full("stsc", 0, 0, u32(entries), table)
}

func sizes(list []uint32) []byte {
	table := make([]byte, 0, 4*len(list))
	for _, s := range list {
		table = binary.BigEndian.AppendUint32(table, s)
	}
	return full("stsz", 0, 0, u32(0), u32(uint32(len(list))), table)
}

func offsets(at []uint64, large bool) []byte {
	if large {
		table := make([]byte, 0, 8*len(at))
		for _, o := range at {
			table = binary.BigEndian.AppendUint64(table, o)
		}
		return full("co64", 0, 0, u32(uint32(len(at))), table)
	}
	table := make([]byte, 0, 4*len(at))
	for _, o := range at {
		table = binary.BigEndian.AppendUint32(table, uint32(o))
	}
	return full("stco", 0, 0, u32(uint32(len(at))), table)
}
